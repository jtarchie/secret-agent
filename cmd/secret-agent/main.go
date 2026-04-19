package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/chat/cli"
	signaltransport "github.com/jtarchie/secret-agent/internal/chat/signal"
	slacktransport "github.com/jtarchie/secret-agent/internal/chat/slack"
	"github.com/jtarchie/secret-agent/internal/config"
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
  secret-agent run --model <provider/model-name> --api-key <key> --config <path> [--skip-preflight]
  secret-agent eval --model <provider/model-name> --api-key <key> [--skip-preflight] [--verbose] <bot.yml>
  secret-agent signal-link --signal-state-dir <path> [--signal-device-name <name>]

examples:
  secret-agent run --model openrouter/anthropic/claude-sonnet-4-5 --api-key $OPENROUTER_API_KEY --config config.yml
  secret-agent eval --model anthropic/claude-sonnet-4-5-20250929 --api-key $ANTHROPIC_API_KEY examples/hello-world.yml
  secret-agent signal-link --signal-state-dir ./signal-state
`

func main() {
	err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing subcommand")
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

//nolint:cyclop // the flag-parse → config-load → bot-wire → transport-wire flow is sequential and clearer as one function
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	modelFlag := fs.String("model", "", "provider/model-name (e.g. openrouter/anthropic/claude-sonnet-4-5)")
	keyFlag := fs.String("api-key", "", "API key for the model provider")
	baseURLFlag := fs.String("base-url", "", "override the provider's base URL (e.g. http://127.0.0.1:1234/v1 for a local OpenAI-compatible server)")
	configFlag := fs.String("config", "", "path to the run config file (bots + transports)")
	skipPreflightFlag := fs.Bool("skip-preflight", false, "skip the startup check that verifies the model endpoint is reachable and the API key is valid")
	mcpPreflightTimeoutFlag := fs.Duration("mcp-preflight-timeout", 5*time.Second, "per-server timeout for the startup MCP tool-listing probe; 0 disables the deadline")
	verboseFlag := fs.Int("verbose", 0, "verbosity: 0 info, 1 debug + signal-cli -v, 2 debug + signal-cli -vv, 3 debug + signal-cli -vvv")
	err := fs.Parse(args)
	if err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *modelFlag == "" || *keyFlag == "" || *configFlag == "" {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("--model, --api-key, and --config are required")
	}

	cfg, err := config.Load(*configFlag)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	provider, name := model.SplitModel(*modelFlag)
	llm, err := model.Resolve(provider, name, *keyFlag, *baseURLFlag)
	if err != nil {
		return fmt.Errorf("resolve model: %w", err)
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

	configDir := filepath.Dir(*configFlag)
	routes := make([]router.Route, 0, len(cfg.Bots))
	for _, p := range cfg.Bots {
		if !filepath.IsAbs(p) {
			p = filepath.Join(configDir, p)
		}
		b, err := bot.Load(p)
		if err != nil {
			return fmt.Errorf("load bot %s: %w", p, err)
		}
		rt, err := runtime.New(ctx, b, llm)
		if err != nil {
			return fmt.Errorf("runtime for bot %q: %w", b.Name, err)
		}
		if !*skipPreflightFlag {
			err := rt.PreflightMCP(ctx, *mcpPreflightTimeoutFlag)
			if err != nil {
				return fmt.Errorf("mcp preflight failed for bot %q (use --skip-preflight to bypass): %w", b.Name, err)
			}
		}
		route, err := router.RouteFromBot(b, rt.HandlerFor)
		if err != nil {
			return fmt.Errorf("route bot %q: %w", b.Name, err)
		}
		routes = append(routes, route)
	}

	rtr, err := router.New(routes, router.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("build router: %w", err)
	}

	transports, err := buildTransports(cfg, logger, routes, *verboseFlag)
	if err != nil {
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, tp := range transports {
		tp := tp
		g.Go(func() error { return tp.Run(gctx, rtr) })
	}
	err = g.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("transports: %w", err)
	}
	return nil
}

// buildTransports instantiates each transport from the config. The routes
// slice is used to pick a bot name for the CLI transport's TUI label.
func buildTransports(cfg *config.Config, logger *slog.Logger, routes []router.Route, verbose int) ([]chat.Transport, error) {
	out := make([]chat.Transport, 0, len(cfg.Transports))
	for _, t := range cfg.Transports {
		switch t.Type {
		case config.TransportCLI:
			if len(routes) > 1 {
				return nil, fmt.Errorf("transport cli requires exactly one bot (got %d)", len(routes))
			}
			out = append(out, cli.New(cli.WithBotName(routes[0].Bot.Name)))
		case config.TransportSignal:
			cmd := t.Command
			if cmd == "" {
				cmd = "signal-cli"
			}
			out = append(out, signaltransport.New(
				t.Account,
				t.StateDir,
				signaltransport.WithCommand(cmd),
				signaltransport.WithLogger(logger),
				signaltransport.WithVerbose(verbose),
			))
		case config.TransportSlack:
			out = append(out, slacktransport.New(
				t.BotToken,
				t.AppToken,
				slacktransport.WithLogger(logger),
			))
		default:
			return nil, fmt.Errorf("unknown transport type %q", t.Type)
		}
	}
	return out, nil
}

//nolint:cyclop // flag parsing + eval result reporting is sequential and clearer as one function
func runEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	modelFlag := fs.String("model", "", "provider/model-name (e.g. anthropic/claude-sonnet-4-5-20250929)")
	keyFlag := fs.String("api-key", "", "API key for the model provider")
	baseURLFlag := fs.String("base-url", "", "override the provider's base URL")
	skipPreflightFlag := fs.Bool("skip-preflight", false, "skip the startup check that verifies the model endpoint is reachable and the API key is valid")
	verboseFlag := fs.Bool("verbose", false, "print observed tool-call trajectory and final text for each case")
	err := fs.Parse(args)
	if err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if *modelFlag == "" || *keyFlag == "" || fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("--model, --api-key, and a bot YAML path are all required")
	}

	path := fs.Arg(0)
	b, err := bot.Load(path)
	if err != nil {
		return fmt.Errorf("load bot: %w", err)
	}
	if len(b.Tests) == 0 {
		return fmt.Errorf("%s: no `tests:` block declared", path)
	}

	provider, name := model.SplitModel(*modelFlag)
	llm, err := model.Resolve(provider, name, *keyFlag, *baseURLFlag)
	if err != nil {
		return fmt.Errorf("resolve model: %w", err)
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
		return fmt.Errorf("run eval: %w", err)
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
	err := fs.Parse(args)
	if err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if *stateDirFlag == "" {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("--signal-state-dir is required")
	}
	err = os.MkdirAll(*stateDirFlag, 0o700)
	if err != nil {
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
		return fmt.Errorf("signal link: %w", err)
	}
	fmt.Fprintf(os.Stderr, "linked account: %s\n", number)
	return nil
}
