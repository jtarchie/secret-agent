package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	adkmodel "google.golang.org/adk/model"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/chat/cli"
	signaltransport "github.com/jtarchie/secret-agent/internal/chat/signal"
	slacktransport "github.com/jtarchie/secret-agent/internal/chat/slack"
	"github.com/jtarchie/secret-agent/internal/config"
	"github.com/jtarchie/secret-agent/internal/model"
	"github.com/jtarchie/secret-agent/internal/router"
	"github.com/jtarchie/secret-agent/internal/runtime"
)

// RunCmd runs bots over configured transports.
type RunCmd struct {
	ModelFlags
	Config              string        `help:"path to the run config file (bots + transports)"                                                 required:""                                                                               type:"existingfile"`
	MCPPreflightTimeout time.Duration `default:"5s"                                                                                           help:"per-server timeout for the startup MCP tool-listing probe; 0 disables the deadline" name:"mcp-preflight-timeout"`
	Verbose             int           `help:"verbosity: 0 info, 1 debug + signal-cli -v, 2 debug + signal-cli -vv, 3 debug + signal-cli -vvv"`
}

//nolint:cyclop,gocognit // the config-load → bot-wire → transport-wire flow is sequential and clearer as one function
func (c *RunCmd) Run() error {
	cfg, err := config.Load(c.Config)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	provider, name := model.SplitModel(c.Model)
	llm, err := model.Resolve(provider, name, c.APIKey, c.BaseURL)
	if err != nil {
		return fmt.Errorf("resolve model: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := newLogger(c.Verbose)

	configDir := filepath.Dir(c.Config)
	topBots := make([]*bot.Bot, 0, len(cfg.Bots))
	for _, p := range cfg.Bots {
		if !filepath.IsAbs(p) {
			p = filepath.Join(configDir, p)
		}
		b, err := bot.Load(p)
		if err != nil {
			return fmt.Errorf("load bot %s: %w", p, err)
		}
		topBots = append(topBots, b)
	}

	// Resolve an LLM per bot (top-level + every sub-agent) with per-bot
	// overrides falling back to the global flags. Memoize by bot pointer so
	// the preflight walk and the runtime resolver share one resolution.
	type endpoint struct{ provider, apiKey, baseURL string }
	llmCache := map[*bot.Bot]adkmodel.LLM{}
	endpoints := map[endpoint]struct{}{}
	for _, b := range topBots {
		var walkErr error
		bot.Walk(b, func(bb *bot.Bot) {
			if walkErr != nil {
				return
			}
			if _, ok := llmCache[bb]; ok {
				return
			}
			botLLM, prov, key, base, err := model.ResolveForBot(bb, llm, provider, name, c.APIKey, c.BaseURL)
			if err != nil {
				walkErr = err
				return
			}
			llmCache[bb] = botLLM
			endpoints[endpoint{prov, key, base}] = struct{}{}
		})
		if walkErr != nil {
			return fmt.Errorf("resolve model: %w", walkErr)
		}
	}

	if !c.SkipPreflight {
		for ep := range endpoints {
			preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := model.Preflight(preflightCtx, ep.provider, ep.apiKey, ep.baseURL)
			cancel()
			if err != nil {
				return fmt.Errorf("model preflight failed (use --skip-preflight to bypass): %w", err)
			}
		}
	}

	resolver := func(bb *bot.Bot) (adkmodel.LLM, error) {
		if got, ok := llmCache[bb]; ok {
			return got, nil
		}
		return llm, nil
	}

	routes := make([]router.Route, 0, len(topBots))
	for _, b := range topBots {
		rt, err := runtime.New(ctx, b, llm, runtime.WithModelResolver(resolver))
		if err != nil {
			return fmt.Errorf("runtime for bot %q: %w", b.Name, err)
		}
		if !c.SkipPreflight {
			err := rt.PreflightMCP(ctx, c.MCPPreflightTimeout)
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

	transports, err := buildTransports(cfg, logger, routes, c.Verbose)
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
			opts := []cli.Option{cli.WithBotName(routes[0].Bot.Name)}
			if t.MessagePrefix != "" {
				opts = append(opts, cli.WithMessagePrefix(t.MessagePrefix))
			}
			out = append(out, cli.New(opts...))
		case config.TransportSignal:
			cmd := t.Command
			if cmd == "" {
				cmd = "signal-cli"
			}
			opts := []signaltransport.Option{
				signaltransport.WithCommand(cmd),
				signaltransport.WithLogger(logger),
				signaltransport.WithVerbose(verbose),
			}
			if t.MessagePrefix != "" {
				opts = append(opts, signaltransport.WithMessagePrefix(t.MessagePrefix))
			}
			out = append(out, signaltransport.New(t.Account, t.StateDir, opts...))
		case config.TransportSlack:
			opts := []slacktransport.Option{slacktransport.WithLogger(logger)}
			if t.MessagePrefix != "" {
				opts = append(opts, slacktransport.WithMessagePrefix(t.MessagePrefix))
			}
			out = append(out, slacktransport.New(t.BotToken, t.AppToken, opts...))
		default:
			return nil, fmt.Errorf("unknown transport type %q", t.Type)
		}
	}
	return out, nil
}
