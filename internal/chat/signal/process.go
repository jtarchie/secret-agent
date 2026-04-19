package signal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// process wraps a running signal-cli jsonRpc subprocess.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

// spawn starts `signal-cli --config <stateDir> [-a <account>] [extraArgs...] jsonRpc`.
// If account is empty, signal-cli auto-selects the only linked account in
// the state dir (useful for the `signal-link` flow before any account exists).
func spawn(ctx context.Context, command, stateDir, account string, extraArgs ...string) (*process, error) {
	if command == "" {
		command = "signal-cli"
	}
	args := []string{}
	if stateDir != "" {
		args = append(args, "--config", stateDir)
	}
	if account != "" {
		args = append(args, "-a", account)
	}
	args = append(args, extraArgs...)
	args = append(args, "jsonRpc")

	cmd := exec.CommandContext(ctx, command, args...)
	// Give signal-cli a grace period to flush ratchet state on shutdown.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	err = cmd.Start()
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", command, err)
	}

	return &process{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
	}, nil
}

// signalCliLogRe matches signal-cli's default log format, which looks like:
//
//	15:38:34.123 [pool-1-thread-1] ERROR org.asamk.signal.jsonrpc.Foo - message
//
// We route each line through slog at the matching level so -v output
// from signal-cli is readable through the same sink as our own events.
var signalCliLogRe = regexp.MustCompile(`^\S+\s+\[.+?\]\s+(TRACE|DEBUG|INFO|WARN|ERROR)\s+(\S+)\s*-\s*(.*)$`)

// forwardStderr drains the subprocess's stderr into slog at a level
// parsed from each line's prefix. Unrecognized lines are emitted at Info.
func (p *process) forwardStderr(ctx context.Context, logger *slog.Logger) {
	scanner := bufio.NewScanner(p.stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if line == "" {
			continue
		}
		if m := signalCliLogRe.FindStringSubmatch(line); m != nil {
			level := parseLogLevel(m[1])
			logger.Log(ctx, level, "signal-cli", "class", m[2], "msg", m[3])
			continue
		}
		logger.Info("signal-cli", "raw", line)
	}
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "TRACE", "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// close shuts down the subprocess. It closes stdin to signal EOF, then waits
// for the process to exit. Caller should have already canceled the context.
func (p *process) close() error {
	_ = p.stdin.Close()
	err := p.cmd.Wait()
	if err != nil {
		return fmt.Errorf("wait signal-cli: %w", err)
	}
	return nil
}
