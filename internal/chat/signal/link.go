package signal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
)

// LinkConfig is input to Link.
type LinkConfig struct {
	// Command is the signal-cli binary. Defaults to "signal-cli".
	Command string
	// StateDir is the directory signal-cli uses for account state.
	// Required.
	StateDir string
	// DeviceName appears on the primary device's linked-devices list.
	// Defaults to "secret-agent".
	DeviceName string
	// Logger receives signal-cli stderr and Link's own events. Defaults to a
	// slog.TextHandler on stderr.
	Logger *slog.Logger
	// URIOut receives the `sgnl://...` linking URI for out-of-band display.
	// Defaults to os.Stdout.
	URIOut io.Writer
	// NoQRCode disables inline QR rendering. The URI is still printed to
	// URIOut regardless. Leave zero-valued to get a QR in the terminal.
	NoQRCode bool
	// QRCodeOut receives the rendered QR block. Defaults to URIOut.
	QRCodeOut io.Writer
}

// Link drives the linked-secondary-device flow end-to-end:
//
//  1. Spawns `signal-cli --config <StateDir> link -n <DeviceName>`.
//  2. Reads the `sgnl://linkdevice?...` URI from stdout, prints it and
//     renders an inline QR code.
//  3. Blocks until the primary device confirms the link and signal-cli exits.
//  4. Returns the linked account's phone number (E.164).
//
// We deliberately use the standalone `link` subcommand rather than the
// jsonRpc `startLink`/`finishLink` pair: upstream has a bug where
// finishLink tries to .add() to an immutable list in the jsonRpc
// dispatcher and fails after the account is saved. The `link` subcommand
// avoids that dispatcher entirely.
func Link(ctx context.Context, cfg LinkConfig) (string, error) {
	if cfg.StateDir == "" {
		return "", fmt.Errorf("StateDir is required")
	}
	if cfg.Command == "" {
		cfg.Command = "signal-cli"
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = "secret-agent"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	log := cfg.Logger.With("component", "signal-link")
	if cfg.URIOut == nil {
		cfg.URIOut = os.Stdout
	}
	if cfg.QRCodeOut == nil {
		cfg.QRCodeOut = cfg.URIOut
	}

	cmd := exec.CommandContext(ctx, cfg.Command, "--config", cfg.StateDir, "link", "-n", cfg.DeviceName)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	log.Info("spawning signal-cli link",
		"command", cfg.Command,
		"state_dir", cfg.StateDir,
		"device_name", cfg.DeviceName,
	)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", cfg.Command, err)
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r\n")
			if line == "" {
				continue
			}
			if m := signalCliLogRe.FindStringSubmatch(line); m != nil {
				log.Log(nil, parseLogLevel(m[1]), "signal-cli", "class", m[2], "msg", m[3])
				continue
			}
			log.Info("signal-cli", "raw", line)
		}
	}()

	// signal-cli link's stdout is typically two lines:
	//   sgnl://linkdevice?uuid=...&pub_key=...
	//   Associated with: +15551234567
	// We print+QR the first, then capture the phone number from subsequent lines.
	var number string
	numberRe := regexp.MustCompile(`(\+\d{6,15})`)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "sgnl://") {
			log.Info("received linking URI", "uri_bytes", len(line))
			fmt.Fprintln(cfg.URIOut, line)
			if !cfg.NoQRCode {
				qrterminal.GenerateWithConfig(line, qrterminal.Config{
					Level:          qrterminal.L,
					Writer:         cfg.QRCodeOut,
					HalfBlocks:     true,
					BlackChar:      qrterminal.BLACK_BLACK,
					WhiteChar:      qrterminal.WHITE_WHITE,
					WhiteBlackChar: qrterminal.WHITE_BLACK,
					BlackWhiteChar: qrterminal.BLACK_WHITE,
					QuietZone:      1,
				})
			}
			log.Info("waiting for primary device to confirm")
			continue
		}
		log.Info("signal-cli", "raw", line)
		if m := numberRe.FindString(line); m != "" && number == "" {
			number = m
		}
	}

	if err := cmd.Wait(); err != nil {
		log.Error("signal-cli link exited with error", "err", err)
		return "", fmt.Errorf("signal-cli link: %w", err)
	}

	if number == "" {
		log.Info("account number not in stdout, falling back to accounts.json")
		number, err = readLatestAccount(cfg.StateDir)
		if err != nil {
			return "", fmt.Errorf("link succeeded but could not determine account number: %w", err)
		}
	}
	log.Info("link complete", "number", number)
	return number, nil
}

// readLatestAccount returns the number from the last entry in
// <stateDir>/data/accounts.json, which signal-cli writes on successful link.
func readLatestAccount(stateDir string) (string, error) {
	path := filepath.Join(stateDir, "data", "accounts.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var parsed struct {
		Accounts []struct {
			Number string `json:"number"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if len(parsed.Accounts) == 0 {
		return "", fmt.Errorf("%s has no accounts", path)
	}
	return parsed.Accounts[len(parsed.Accounts)-1].Number, nil
}
