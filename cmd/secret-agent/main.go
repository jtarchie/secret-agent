package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/chat/cli"
	signaltransport "github.com/jtarchie/secret-agent/internal/chat/signal"
	"github.com/jtarchie/secret-agent/internal/eval"
	"github.com/jtarchie/secret-agent/internal/model"
	"github.com/jtarchie/secret-agent/internal/router"
	"github.com/jtarchie/secret-agent/internal/runtime"
)

// newLogger builds a text slog.Logger on stderr. verbose>=1 enables Debug.
func newLogger(verbose int) *slog.Logger {
	level := slog.LevelInfo
	if verbose >= 1 {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

const usage = `usage:
  secret-agent run --model <provider/model-name> --api-key <key> [--transport cli|signal] [--skip-preflight] [signal flags] <bot.yml> [bot.yml ...]
  secret-agent eval --model <provider/model-name> --api-key <key> [--skip-preflight] [--verbose] <bot.yml>
  secret-agent signal-link --signal-state-dir <path> [--signal-device-name <name>]

examples:
  secret-agent run --model openrouter/anthropic/claude-sonnet-4-5 --api-key $OPENROUTER_API_KEY examples/hello-world.yml
  secret-agent run --model anthropic/claude-sonnet-4-5-20250929 --api-key $ANTHROPIC_API_KEY \
      --transport signal --signal-account +15551234567 --signal-state-dir ./signal-state \
      examples/admin-bot.yml examples/public-bot.yml
  secret-agent eval --model anthropic/claude-sonnet-4-5-20250929 --api-key $ANTHROPIC_API_KEY examples/hello-world.yml
  secret-agent signal-link --signal-state-dir ./signal-state
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("missing subcommand")
	}
	switch args[0] {
	case "run":
		return runRun(args[1:])
	case "eval":
		return runEval(args[1:])
	case "signal-link":
		return runSignalLink(args[1:])
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	modelFlag := fs.String("model", "", "provider/model-name (e.g. openrouter/anthropic/claude-sonnet-4-5)")
	keyFlag := fs.String("api-key", "", "API key for the model provider")
	baseURLFlag := fs.String("base-url", "", "override the provider's base URL (e.g. http://127.0.0.1:1234/v1 for a local OpenAI-compatible server)")
	transportFlag := fs.String("transport", "cli", "chat transport: cli | signal")
	signalAccountFlag := fs.String("signal-account", "", "Signal phone number (E.164) for --transport=signal")
	signalStateDirFlag := fs.String("signal-state-dir", "", "directory for signal-cli state (keys, ratchet state); required for --transport=signal")
	signalCmdFlag := fs.String("signal-cli", "signal-cli", "path to the signal-cli binary")
	skipPreflightFlag := fs.Bool("skip-preflight", false, "skip the startup check that verifies the model endpoint is reachable and the API key is valid")
	mcpPreflightTimeoutFlag := fs.Duration("mcp-preflight-timeout", 5*time.Second, "per-server timeout for the startup MCP tool-listing probe; 0 disables the deadline")
	verboseFlag := fs.Int("verbose", 0, "verbosity: 0 info, 1 debug + signal-cli -v, 2 debug + signal-cli -vv, 3 debug + signal-cli -vvv")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *modelFlag == "" || *keyFlag == "" || fs.NArg() < 1 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("--model, --api-key, and at least one bot YAML path are required")
	}

	paths := fs.Args()
	if *transportFlag == "cli" && len(paths) > 1 {
		return fmt.Errorf("--transport=cli only supports a single bot YAML (got %d)", len(paths))
	}

	provider, name := model.SplitModel(*modelFlag)
	llm, err := model.Resolve(provider, name, *keyFlag, *baseURLFlag)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !*skipPreflightFlag {
		preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := model.Preflight(preflightCtx, provider, *keyFlag, *baseURLFlag)
		cancel()
		if err != nil {
			return fmt.Errorf("model preflight failed (use --skip-preflight to bypass): %w", err)
		}
	}

	logger := newLogger(*verboseFlag)

	routes := make([]router.Route, 0, len(paths))
	for _, p := range paths {
		b, err := bot.Load(p)
		if err != nil {
			return err
		}
		rt, err := runtime.New(ctx, b, llm)
		if err != nil {
			return fmt.Errorf("runtime for bot %q: %w", b.Name, err)
		}
		if !*skipPreflightFlag {
			if err := rt.PreflightMCP(ctx, *mcpPreflightTimeoutFlag); err != nil {
				return fmt.Errorf("mcp preflight failed for bot %q (use --skip-preflight to bypass): %w", b.Name, err)
			}
		}
		route, err := router.RouteFromBot(b, rt.HandlerFor)
		if err != nil {
			return err
		}
		routes = append(routes, route)
	}

	rtr, err := router.New(routes, router.WithLogger(logger))
	if err != nil {
		return err
	}

	primaryName := routes[0].Bot.Name

	var transport chat.Transport
	switch *transportFlag {
	case "cli":
		transport = cli.New()
	case "signal":
		if *signalAccountFlag == "" || *signalStateDirFlag == "" {
			return fmt.Errorf("--transport=signal requires --signal-account and --signal-state-dir")
		}
		transport = signaltransport.New(
			*signalAccountFlag,
			*signalStateDirFlag,
			signaltransport.WithCommand(*signalCmdFlag),
			signaltransport.WithLogger(logger),
			signaltransport.WithVerbose(*verboseFlag),
		)
	default:
		return fmt.Errorf("unknown --transport %q (want cli or signal)", *transportFlag)
	}

	return transport.Run(ctx, primaryName, rtr)
}

func runEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	modelFlag := fs.String("model", "", "provider/model-name (e.g. anthropic/claude-sonnet-4-5-20250929)")
	keyFlag := fs.String("api-key", "", "API key for the model provider")
	baseURLFlag := fs.String("base-url", "", "override the provider's base URL")
	skipPreflightFlag := fs.Bool("skip-preflight", false, "skip the startup check that verifies the model endpoint is reachable and the API key is valid")
	verboseFlag := fs.Bool("verbose", false, "print observed tool-call trajectory and final text for each case")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *modelFlag == "" || *keyFlag == "" || fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("--model, --api-key, and a bot YAML path are all required")
	}

	path := fs.Arg(0)
	b, err := bot.Load(path)
	if err != nil {
		return err
	}
	if len(b.Tests) == 0 {
		return fmt.Errorf("%s: no `tests:` block declared", path)
	}

	provider, name := model.SplitModel(*modelFlag)
	llm, err := model.Resolve(provider, name, *keyFlag, *baseURLFlag)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !*skipPreflightFlag {
		preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := model.Preflight(preflightCtx, provider, *keyFlag, *baseURLFlag)
		cancel()
		if err != nil {
			return fmt.Errorf("model preflight failed (use --skip-preflight to bypass): %w", err)
		}
	}

	results, err := eval.RunAll(ctx, b, llm)
	if err != nil {
		return err
	}

	passed, failed := 0, 0
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
			failed++
		} else {
			passed++
		}
		fmt.Printf("%s  %s  (%s)\n", status, r.Name, r.Duration.Round(time.Millisecond))
		if *verboseFlag || !r.Passed {
			if len(r.ToolCalls) > 0 {
				names := make([]string, len(r.ToolCalls))
				for i, c := range r.ToolCalls {
					names[i] = c.Name
				}
				fmt.Printf("      tools: %v\n", names)
			}
			if r.FinalText != "" {
				fmt.Printf("      output: %q\n", r.FinalText)
			}
		}
		for _, f := range r.Failures {
			fmt.Printf("      - %s\n", f)
		}
	}
	fmt.Printf("\n%d passed, %d failed (of %d)\n", passed, failed, len(results))
	if failed > 0 {
		return fmt.Errorf("%d test(s) failed", failed)
	}
	return nil
}

func runSignalLink(args []string) error {
	fs := flag.NewFlagSet("signal-link", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stateDirFlag := fs.String("signal-state-dir", "", "directory for signal-cli state (will be created if missing)")
	deviceNameFlag := fs.String("signal-device-name", "secret-agent", "device name shown on the primary device")
	cmdFlag := fs.String("signal-cli", "signal-cli", "path to the signal-cli binary")
	noQRFlag := fs.Bool("no-qr", false, "do not render a QR code in the terminal; only print the linking URI")
	verboseFlag := fs.Int("verbose", 0, "verbosity: 0 info, 1 debug")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stateDirFlag == "" {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("--signal-state-dir is required")
	}
	if err := os.MkdirAll(*stateDirFlag, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *noQRFlag {
		fmt.Fprintln(os.Stderr, "Scan the URI below as a QR code in your Signal primary device (Linked devices → Link new device):")
	} else {
		fmt.Fprintln(os.Stderr, "Scan the QR code below with your Signal primary device (Linked devices → Link new device). The raw URI is printed first as a fallback.")
	}
	number, err := signaltransport.Link(ctx, signaltransport.LinkConfig{
		Command:    *cmdFlag,
		StateDir:   *stateDirFlag,
		DeviceName: *deviceNameFlag,
		NoQRCode:   *noQRFlag,
		Logger:     newLogger(*verboseFlag),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "linked account: %s\n", number)
	return nil
}
