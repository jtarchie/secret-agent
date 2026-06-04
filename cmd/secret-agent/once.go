package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	adkmodel "google.golang.org/adk/model"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/model"
	"github.com/jtarchie/secret-agent/internal/runtime"
	"github.com/jtarchie/secret-agent/internal/tool"
)

// errNoModel is returned when neither the flags nor the agent YAML name a model
// to resolve. Sentinel so callers/tests can match it.
var errNoModel = errors.New("no model: pass --model and --api-key, or declare model:/api_key_env: in the YAML")

// OnceCmd runs a single non-interactive agent turn from one YAML and prints the
// final assistant text. It bypasses the router and all transports — intended
// for automation loops. Model/api-key are optional because the agent YAML may
// declare its own model:/api_key_env:/base_url:.
type OnceCmd struct {
	Bot string `arg:"" help:"path to the agent YAML" type:"existingfile"`

	Message string   `help:"user message; if omitted, read from stdin when piped" name:"message" short:"m"`
	Attach  []string `help:"attach a file (repeatable)"                           name:"attach"  short:"a" type:"existingfile"`

	Output string `help:"write the reply to FILE instead of stdout"           name:"output" short:"o"`
	JSON   bool   `help:"emit a structured JSON object instead of plain text" name:"json"`

	Model         string `help:"provider/model-name fallback when the YAML omits model:"              name:"model"`
	APIKey        string `help:"API key fallback when the YAML omits api_key_env:"                    name:"api-key"`
	BaseURL       string `help:"override the provider's base URL"                                     name:"base-url"`
	SkipPreflight bool   `help:"skip model + MCP reachability checks (use in tight automation loops)" name:"skip-preflight"`

	SenderPhone string        `help:"value exposed to tools as $SENDER_PHONE"             name:"sender-phone"`
	Timeout     time.Duration `help:"abort the turn after this duration (0 = no timeout)" name:"timeout"`
	Verbose     int           `help:"0 info, >=1 debug + per-tool-call trace on stderr"   short:"v"           type:"counter"`
}

func (c *OnceCmd) Run() error {
	b, err := bot.Load(c.Bot)
	if err != nil {
		return fmt.Errorf("load bot: %w", err)
	}

	msg, err := resolveMessage(c.Message, c.Attach, os.Stdin)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	logger := newLogger(c.Verbose)

	res, err := c.buildRuntime(ctx, b)
	if err != nil {
		return err
	}

	rec, snapshot := newRecorder(logger, c.Verbose >= 1)
	rt, err := runtime.New(ctx, b, res.defaultLLM,
		runtime.WithModelResolver(res.resolver),
		runtime.WithToolRecorder(rec),
	)
	if err != nil {
		return fmt.Errorf("build runtime: %w", err)
	}
	if !c.SkipPreflight {
		err = rt.PreflightMCP(ctx, 5*time.Second)
		if err != nil {
			return fmt.Errorf("mcp preflight failed (use --skip-preflight to bypass): %w", err)
		}
	}

	turnCtx := tool.WithSenderPhone(ctx, c.SenderPhone)
	handler := rt.HandlerFor("once")

	var reply strings.Builder
	var streamErr error
	for ch := range handler(turnCtx, msg) {
		if ch.Err != nil {
			streamErr = ch.Err
			continue
		}
		reply.WriteString(ch.Delta)
	}

	return c.writeResult(reply.String(), snapshot(), streamErr)
}

// resolved bundles the per-bot model resolution outputs so Run stays flat.
type resolved struct {
	defaultLLM adkmodel.LLM
	resolver   runtime.ModelResolver
}

// buildRuntime resolves the LLM for the bot tree (flags as fallback under the
// YAML's own overrides) and preflights the endpoints unless skipped. It returns
// the default LLM plus a per-bot resolver for runtime.New.
func (c *OnceCmd) buildRuntime(ctx context.Context, b *bot.Bot) (resolved, error) {
	defaultLLM, provider, name, err := resolveDefaultLLM(c.Model, c.APIKey, c.BaseURL)
	if err != nil {
		return resolved{}, err
	}

	llmCache, endpoints, err := resolveBotLLMs([]*bot.Bot{b}, defaultLLM, provider, name, c.APIKey, c.BaseURL)
	if err != nil {
		return resolved{}, fmt.Errorf("resolve model: %w", err)
	}
	if defaultLLM == nil && llmCache[b] == nil {
		return resolved{}, errNoModel
	}

	if !c.SkipPreflight {
		err = preflightEndpoints(ctx, endpoints)
		if err != nil {
			return resolved{}, err
		}
	}

	resolver := func(bb *bot.Bot) (adkmodel.LLM, error) {
		if got, ok := llmCache[bb]; ok {
			return got, nil
		}
		return defaultLLM, nil
	}
	return resolved{defaultLLM: defaultLLM, resolver: resolver}, nil
}

// resolveDefaultLLM builds the flag-derived default LLM. When no --model is
// given it returns (nil, "", "", nil); callers then rely on per-bot YAML
// overrides via resolveBotLLMs.
func resolveDefaultLLM(modelFlag, apiKey, baseURL string) (adkmodel.LLM, string, string, error) {
	if modelFlag == "" {
		return nil, "", "", nil
	}
	provider, name := model.SplitModel(modelFlag)
	llm, err := model.Resolve(provider, name, apiKey, baseURL)
	if err != nil {
		return nil, "", "", fmt.Errorf("resolve model: %w", err)
	}
	return llm, provider, name, nil
}

// resolveMessage applies input precedence: -m wins; else read piped stdin; else
// allow empty text only when attachments exist; else error. stdin is passed in
// (rather than read from os.Stdin) so the helper is unit-testable.
func resolveMessage(flag string, attach []string, stdin *os.File) (chat.Message, error) {
	text := flag
	if text == "" && isPiped(stdin) {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return chat.Message{}, fmt.Errorf("read stdin: %w", err)
		}
		text = strings.TrimRight(string(raw), "\n")
	}
	atts := buildAttachments(attach)
	if text == "" && len(atts) == 0 {
		return chat.Message{}, errors.New("no message: pass -m, pipe stdin, or provide -a")
	}
	return chat.Message{Text: text, Attachments: atts}, nil
}

// isPiped reports whether stdin is a pipe/redirect rather than an interactive
// terminal.
func isPiped(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}

// buildAttachments turns file paths into chat.Attachments. ContentType is left
// empty so tool.BuildAttachedContent sniffs it (text inlined, binary base64'd).
func buildAttachments(paths []string) []chat.Attachment {
	if len(paths) == 0 {
		return nil
	}
	out := make([]chat.Attachment, 0, len(paths))
	for _, p := range paths {
		out = append(out, chat.Attachment{Path: p, Filename: filepath.Base(p)})
	}
	return out
}

// newRecorder returns a runtime.ToolRecorder that accumulates tool calls (for
// --json) and, when trace is set, logs each call to stderr. snapshot returns a
// copy of the calls observed so far.
func newRecorder(logger *slog.Logger, trace bool) (runtime.ToolRecorder, func() []runtime.ToolCall) {
	var (
		mu    sync.Mutex
		calls []runtime.ToolCall
	)
	rec := func(c runtime.ToolCall) {
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		if trace {
			logger.Debug("tool call", "name", c.Name, "args", c.Args, "err", c.ErrMsg)
		}
	}
	snapshot := func() []runtime.ToolCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]runtime.ToolCall, len(calls))
		copy(out, calls)
		return out
	}
	return rec, snapshot
}

// writeResult buffers the full reply and writes it exactly once, so a mid-stream
// error never leaves a partial -o file. In text mode a stream error suppresses
// output and is surfaced (exit 1); in JSON mode the object is emitted with the
// error populated, then the error is still returned for a non-zero exit code.
func (c *OnceCmd) writeResult(text string, calls []runtime.ToolCall, streamErr error) error {
	var payload []byte
	if c.JSON {
		payload = marshalOnceJSON(text, calls, streamErr)
	} else {
		if streamErr != nil {
			return fmt.Errorf("turn failed: %w", streamErr)
		}
		payload = []byte(text + "\n")
	}

	if c.Output != "" {
		err := os.WriteFile(c.Output, payload, 0o600)
		if err != nil {
			return fmt.Errorf("write %s: %w", c.Output, err)
		}
	} else {
		_, err := os.Stdout.Write(payload)
		if err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
	}

	if c.JSON && streamErr != nil {
		return fmt.Errorf("turn failed: %w", streamErr)
	}
	return nil
}

type onceToolCall struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args"`
	Result map[string]any `json:"result"`
	Error  string         `json:"error"`
}

type onceOutput struct {
	Output    string         `json:"output"`
	ToolCalls []onceToolCall `json:"tool_calls"`
	Error     *string        `json:"error"`
}

// marshalOnceJSON renders the structured --json payload. It never fails: the
// values are plain Go types that always marshal.
func marshalOnceJSON(text string, calls []runtime.ToolCall, streamErr error) []byte {
	tcs := make([]onceToolCall, 0, len(calls))
	for _, c := range calls {
		tcs = append(tcs, onceToolCall{Name: c.Name, Args: c.Args, Result: c.Result, Error: c.ErrMsg})
	}
	var errStr *string
	if streamErr != nil {
		s := streamErr.Error()
		errStr = &s
	}
	out, _ := json.MarshalIndent(onceOutput{Output: text, ToolCalls: tcs, Error: errStr}, "", "  ")
	return append(out, '\n')
}
