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
	imessagetransport "github.com/jtarchie/secret-agent/internal/chat/imessage"
	signaltransport "github.com/jtarchie/secret-agent/internal/chat/signal"
	slacktransport "github.com/jtarchie/secret-agent/internal/chat/slack"
	"github.com/jtarchie/secret-agent/internal/config"
	cronpkg "github.com/jtarchie/secret-agent/internal/cron"
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

	topBots, err := loadTopBots(cfg.Bots, filepath.Dir(c.Config))
	if err != nil {
		return err
	}

	llmCache, endpoints, err := resolveBotLLMs(topBots, llm, provider, name, c.APIKey, c.BaseURL)
	if err != nil {
		return err
	}

	if !c.SkipPreflight {
		err := preflightEndpoints(ctx, endpoints)
		if err != nil {
			return err
		}
	}

	resolver := func(bb *bot.Bot) (adkmodel.LLM, error) {
		if got, ok := llmCache[bb]; ok {
			return got, nil
		}
		return llm, nil
	}

	transports, err := buildTransports(cfg, logger, topBots, c.Verbose)
	if err != nil {
		return err
	}
	senders := buildSenderRegistry(cfg, transports)

	scheduler := cronpkg.New(logger, senders)
	routes, err := buildRoutes(ctx, topBots, llm, resolver, senders, scheduler, c.SkipPreflight, c.MCPPreflightTimeout)
	if err != nil {
		return err
	}

	rtr, err := router.New(routes, router.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("build router: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, tp := range transports {
		tp := tp
		g.Go(func() error { return tp.Run(gctx, rtr) })
	}
	if scheduler.HasJobs() {
		g.Go(func() error { return scheduler.Run(gctx) })
	}
	err = g.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("transports: %w", err)
	}
	return nil
}

// loadTopBots loads each bot YAML named in the config, resolving relative
// paths against configDir.
func loadTopBots(paths []string, configDir string) ([]*bot.Bot, error) {
	out := make([]*bot.Bot, 0, len(paths))
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(configDir, p)
		}
		b, err := bot.Load(p)
		if err != nil {
			return nil, fmt.Errorf("load bot %s: %w", p, err)
		}
		out = append(out, b)
	}
	return out, nil
}

type modelEndpoint struct{ provider, apiKey, baseURL string }

// resolveBotLLMs walks every bot in the tree and resolves an LLM per bot,
// memoized by bot pointer. Returns the cache and the unique set of
// (provider, key, base) tuples observed for preflight.
func resolveBotLLMs(
	topBots []*bot.Bot,
	defaultLLM adkmodel.LLM,
	provider, name, apiKey, baseURL string,
) (map[*bot.Bot]adkmodel.LLM, map[modelEndpoint]struct{}, error) {
	llmCache := map[*bot.Bot]adkmodel.LLM{}
	endpoints := map[modelEndpoint]struct{}{}
	for _, b := range topBots {
		var walkErr error
		bot.Walk(b, func(bb *bot.Bot) {
			if walkErr != nil {
				return
			}
			if _, ok := llmCache[bb]; ok {
				return
			}
			botLLM, prov, key, base, err := model.ResolveForBot(bb, defaultLLM, provider, name, apiKey, baseURL)
			if err != nil {
				walkErr = err
				return
			}
			llmCache[bb] = botLLM
			endpoints[modelEndpoint{prov, key, base}] = struct{}{}
		})
		if walkErr != nil {
			return nil, nil, fmt.Errorf("resolve model: %w", walkErr)
		}
	}
	return llmCache, endpoints, nil
}

// preflightEndpoints runs the model preflight against each unique endpoint.
func preflightEndpoints(ctx context.Context, endpoints map[modelEndpoint]struct{}) error {
	for ep := range endpoints {
		preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := model.Preflight(preflightCtx, ep.provider, ep.apiKey, ep.baseURL)
		cancel()
		if err != nil {
			return fmt.Errorf("model preflight failed (use --skip-preflight to bypass): %w", err)
		}
	}
	return nil
}

// buildRoutes constructs a runtime + router.Route per bot, registers each
// runtime with the scheduler, and runs MCP preflight unless skipped.
func buildRoutes(
	ctx context.Context,
	topBots []*bot.Bot,
	llm adkmodel.LLM,
	resolver runtime.ModelResolver,
	senders chat.SenderRegistry,
	scheduler *cronpkg.Scheduler,
	skipPreflight bool,
	mcpPreflightTimeout time.Duration,
) ([]router.Route, error) {
	routes := make([]router.Route, 0, len(topBots))
	for _, b := range topBots {
		rt, err := runtime.New(ctx, b, llm,
			runtime.WithModelResolver(resolver),
			runtime.WithSenderRegistry(senders),
		)
		if err != nil {
			return nil, fmt.Errorf("runtime for bot %q: %w", b.Name, err)
		}
		if !skipPreflight {
			err := rt.PreflightMCP(ctx, mcpPreflightTimeout)
			if err != nil {
				return nil, fmt.Errorf("mcp preflight failed for bot %q (use --skip-preflight to bypass): %w", b.Name, err)
			}
		}
		route, err := router.RouteFromBot(b, rt.HandlerFor)
		if err != nil {
			return nil, fmt.Errorf("route bot %q: %w", b.Name, err)
		}
		routes = append(routes, route)
		//nolint:contextcheck // cron jobs create their own context.Background() at fire time; registration ctx would be long-canceled
		err = scheduler.Register(b, rt)
		if err != nil {
			return nil, fmt.Errorf("register cron for bot %q: %w", b.Name, err)
		}
	}
	return routes, nil
}

// buildSenderRegistry walks the configured transports and maps each
// transport's `type:` string to its chat.Sender. Transports that can
// send (Signal/Slack/iMessage) are indexed; the CLI transport is also
// indexed so tools can discover it exists — its Send returns
// chat.ErrSendUnsupported at call time.
func buildSenderRegistry(cfg *config.Config, transports []chat.Transport) chat.SenderRegistry {
	reg := make(chat.SenderRegistry, len(transports))
	for i, t := range cfg.Transports {
		if i >= len(transports) {
			break
		}
		sender, ok := transports[i].(chat.Sender)
		if !ok {
			continue
		}
		reg[string(t.Type)] = sender
	}
	return reg
}

// buildTransports instantiates each transport from the config. topBots is
// used to pick a bot name for the CLI transport's TUI label.
func buildTransports(cfg *config.Config, logger *slog.Logger, topBots []*bot.Bot, verbose int) ([]chat.Transport, error) {
	out := make([]chat.Transport, 0, len(cfg.Transports))
	for _, t := range cfg.Transports {
		switch t.Type {
		case config.TransportCLI:
			if len(topBots) > 1 {
				return nil, fmt.Errorf("transport cli requires exactly one bot (got %d)", len(topBots))
			}
			opts := []cli.Option{cli.WithBotName(topBots[0].Name)}
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
		case config.TransportIMessage:
			opts := []imessagetransport.Option{imessagetransport.WithLogger(logger)}
			if t.PollInterval != "" {
				d, err := time.ParseDuration(t.PollInterval)
				if err != nil {
					return nil, fmt.Errorf("imessage poll_interval %q: %w", t.PollInterval, err)
				}
				opts = append(opts, imessagetransport.WithPollInterval(d))
			}
			if t.MessagePrefix != "" {
				opts = append(opts, imessagetransport.WithMessagePrefix(t.MessagePrefix))
			}
			out = append(out, imessagetransport.New(t.DatabasePath, t.StateDir, opts...))
		default:
			return nil, fmt.Errorf("unknown transport type %q", t.Type)
		}
	}
	return out, nil
}
