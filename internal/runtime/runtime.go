// Package runtime wires a Bot into an ADK runner and exposes a simple Send API.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/hook"
	"github.com/jtarchie/secret-agent/internal/tool"
)

// ToolCall is a single observed tool invocation captured by a ToolRecorder.
// Args is the arguments the LLM passed in; Result is what the tool returned
// (or the replacement result from a hook); ErrMsg is non-empty if the tool
// (or a before_tool hook) errored.
type ToolCall struct {
	Name   string
	Args   map[string]any
	Result map[string]any
	ErrMsg string
}

// ToolRecorder is invoked once per tool call on the top-level agent after
// the call completes. It fires on both success and failure. Sub-agent
// internals are not surfaced — sub-agents are wrapped as tools at the
// parent level, so their invocation is recorded as a single tool call.
type ToolRecorder func(ToolCall)

// Option customizes Runtime construction. Pass zero or more to New.
type Option func(*options)

type options struct {
	recorder ToolRecorder
	resolver ModelResolver
}

// WithToolRecorder attaches a callback that fires after every tool call on
// the root agent. Intended for eval/test capture; behavior is unchanged
// (the callback cannot mutate args or results).
func WithToolRecorder(fn ToolRecorder) Option {
	return func(o *options) { o.recorder = fn }
}

// ModelResolver returns the LLM to use for a given bot. Called once per bot
// in the tree (top-level and every sub-agent). The returned LLM is used for
// that bot's llmagent and is also threaded down as the inherited default
// for any of its sub-agents that do not resolve their own override.
type ModelResolver func(*bot.Bot) (adkmodel.LLM, error)

// WithModelResolver installs a per-bot model resolver. Without it, every
// bot in the tree uses the LLM passed to New.
func WithModelResolver(r ModelResolver) Option {
	return func(o *options) { o.resolver = r }
}

type Runtime struct {
	appName  string
	sessions session.Service
	runner   *runner.Runner

	mu    sync.Mutex
	known map[string]struct{}

	stateless bool
	turnSeq   atomic.Uint64

	mcpProbes []mcpProbe
}

// mcpProbe identifies one MCP toolset constructed during buildAgent so
// PreflightMCP can exercise it.
type mcpProbe struct {
	agent   string // agent name this MCP server belongs to
	server  string // mcp server name as declared in YAML
	toolset adktool.Toolset
}

func New(ctx context.Context, b *bot.Bot, llm adkmodel.LLM, opts ...Option) (*Runtime, error) {
	cfg := options{}
	for _, o := range opts {
		o(&cfg)
	}

	_ = ctx // reserved for future use; setup performs no I/O that needs cancellation
	bld := &builder{recorder: cfg.recorder, resolver: cfg.resolver}
	//nolint:contextcheck // buildAgent is pure wiring — no I/O to cancel, so ctx is intentionally not threaded through
	root, err := bld.buildAgent(b.Name, fmt.Sprintf("YAML-defined bot %q", b.Name), b, llm, true)
	if err != nil {
		return nil, err
	}

	sessions := session.InMemoryService()

	r, err := runner.New(runner.Config{
		AppName:        b.Name,
		Agent:          root,
		SessionService: sessions,
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}

	return &Runtime{
		appName:   b.Name,
		sessions:  sessions,
		runner:    r,
		known:     map[string]struct{}{},
		stateless: b.Permissions.MemoryOrDefault() == bot.MemoryNone,
		mcpProbes: bld.probes,
	}, nil
}

// builder threads a shared probe slice through the recursive buildAgent so
// MCP servers declared anywhere in the bot tree can be preflighted together.
type builder struct {
	probes   []mcpProbe
	recorder ToolRecorder
	resolver ModelResolver
}

// buildAgent constructs an ADK llmagent for a bot, recursively wrapping each
// declared sub-agent as an AgentTool so the parent LLM can invoke it by name.
// The provided name/description override the bot's own so parent-defined map
// keys and descriptions drive what the parent LLM sees. isRoot is true only
// for the top-level call so eval recorders fire once per turn at the outer
// boundary rather than once per sub-agent level.
//
//nolint:cyclop // sequential wiring of MCP toolsets, tools, sub-agents, hooks, and recorders reads clearly top-to-bottom
func (bld *builder) buildAgent(name, description string, b *bot.Bot, llm adkmodel.LLM, isRoot bool) (agent.Agent, error) {
	effective := llm
	if bld.resolver != nil {
		got, err := bld.resolver(b)
		if err != nil {
			return nil, fmt.Errorf("resolve model for %q: %w", b.Name, err)
		}
		if got != nil {
			effective = got
		}
	}

	toolsets := make([]adktool.Toolset, 0, len(b.MCP))
	for _, m := range b.MCP {
		ts, err := tool.NewMCP(m)
		if err != nil {
			return nil, fmt.Errorf("mcp %q: %w", m.Name, err)
		}
		toolsets = append(toolsets, ts)
		bld.probes = append(bld.probes, mcpProbe{agent: name, server: m.Name, toolset: ts})
	}

	tools := make([]adktool.Tool, 0, len(b.Tools)+len(b.Agents))
	for _, t := range b.Tools {
		var (
			built adktool.Tool
			err   error
		)
		switch {
		case t.Sh != "":
			built, err = tool.NewShell(t.Name, t.Description, t.Sh, t.Params, t.Returns)
		case t.Expr != "":
			built, err = tool.NewExpr(t.Name, t.Description, t.Expr, t.Params)
		case t.Js != "":
			built, err = tool.NewJs(t.Name, t.Description, t.Js, t.Params)
		default:
			return nil, fmt.Errorf("tool %q: no runtime (sh/expr/js) set", t.Name)
		}
		if err != nil {
			return nil, fmt.Errorf("tool %q: %w", t.Name, err)
		}
		tools = append(tools, built)
	}

	for key, ref := range b.Agents {
		childDesc := ref.Description
		if childDesc == "" {
			childDesc = fmt.Sprintf("Sub-agent %q", key)
		}
		child, err := bld.buildAgent(key, childDesc, ref.Bot, effective, false)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", key, err)
		}
		wrapped, err := tool.NewSubAgent(key, childDesc, child, ref.SkipSummarization, ref.Attachments)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", key, err)
		}
		tools = append(tools, wrapped)
	}

	compiled, err := hook.Compile(b.Hooks)
	if err != nil {
		return nil, fmt.Errorf("compile bot hooks: %w", err)
	}
	for _, t := range b.Tools {
		th, err := hook.Compile(t.Hooks)
		if err != nil {
			return nil, fmt.Errorf("compile tool %q hooks: %w", t.Name, err)
		}
		for i := range th {
			th[i].Filter = t.Name
		}
		compiled = append(compiled, th...)
	}
	cbs := hook.BuildLLMCallbacks(compiled)

	if isRoot && bld.recorder != nil {
		cbs.AfterTool = append(cbs.AfterTool, recorderAfterTool(bld.recorder))
	}

	built, err := llmagent.New(llmagent.Config{
		Name:                 name,
		Description:          description,
		Model:                effective,
		Instruction:          b.System,
		Tools:                tools,
		Toolsets:             toolsets,
		BeforeModelCallbacks: cbs.BeforeModel,
		AfterModelCallbacks:  cbs.AfterModel,
		BeforeToolCallbacks:  cbs.BeforeTool,
		AfterToolCallbacks:   cbs.AfterTool,
		BeforeAgentCallbacks: cbs.BeforeAgent,
		AfterAgentCallbacks:  cbs.AfterAgent,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent %q: %w", name, err)
	}
	return built, nil
}

// PreflightMCP exercises every MCP toolset collected during buildAgent in
// parallel, each bounded by timeout, and returns the first error it sees.
// A zero timeout disables the per-probe deadline. Returns nil if no MCP
// servers are declared.
//
// The successful session a probe opens is cached by mcptoolset, so the
// agent's first chat turn reuses it instead of dialing again.
func (r *Runtime) PreflightMCP(ctx context.Context, timeout time.Duration) error {
	if len(r.mcpProbes) == 0 {
		return nil
	}

	type result struct {
		probe mcpProbe
		err   error
	}
	results := make(chan result, len(r.mcpProbes))

	var wg sync.WaitGroup
	for _, p := range r.mcpProbes {
		wg.Add(1)
		go func(p mcpProbe) {
			defer wg.Done()
			probeCtx := ctx
			if timeout > 0 {
				var cancel context.CancelFunc
				probeCtx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			err := tool.PreflightMCP(probeCtx, p.toolset)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					err = fmt.Errorf("preflight timed out after %s: %w", timeout, err)
				}
				err = fmt.Errorf("mcp %q on agent %q: %w", p.server, p.agent, err)
			}
			results <- result{probe: p, err: err}
		}(p)
	}
	wg.Wait()
	close(results)

	for res := range results {
		if res.err != nil {
			return res.err
		}
	}
	return nil
}

// HandlerFor returns a per-turn handler bound to the given conversation ID.
// The underlying ADK session is created lazily (in-memory) on first use.
// In stateless mode each turn gets its own session ID so no history
// accumulates across turns.
//
//nolint:gocognit // the per-conv streaming handler keeps session setup, cancellation, and event pumping in one place
func (r *Runtime) HandlerFor(convID string) func(context.Context, chat.Message) <-chan chat.Chunk {
	return func(ctx context.Context, userMsg chat.Message) <-chan chat.Chunk {
		out := make(chan chat.Chunk)

		go func() {
			defer close(out)

			emit := func(c chat.Chunk) bool {
				select {
				case out <- c:
					return true
				case <-ctx.Done():
					return false
				}
			}

			sessionID := convID
			if r.stateless {
				sessionID = fmt.Sprintf("%s#t%d", convID, r.turnSeq.Add(1))
			}

			err := r.ensureSession(ctx, sessionID)
			if err != nil {
				emit(chat.Chunk{Err: err})
				return
			}

			msg, err := buildUserContent(userMsg)
			if err != nil {
				emit(chat.Chunk{Err: err})
				return
			}
			runCtx := tool.WithAttachments(ctx, userMsg.Attachments)
			for ev, err := range r.runner.Run(runCtx, sessionID, sessionID, msg, agent.RunConfig{}) {
				if err != nil {
					emit(chat.Chunk{Err: err})
					return
				}
				if ev.Content == nil {
					continue
				}
				for _, p := range ev.Content.Parts {
					if p.Text == "" {
						continue
					}
					if !emit(chat.Chunk{Delta: p.Text}) {
						return
					}
				}
			}
		}()

		return out
	}
}

// buildUserContent composes a user-role genai.Content from a chat.Message by
// delegating to the shared tool.BuildAttachedContent helper.
func buildUserContent(msg chat.Message) (*genai.Content, error) {
	c, err := tool.BuildAttachedContent(msg.Text, msg.Attachments)
	if err != nil {
		return nil, fmt.Errorf("build user content: %w", err)
	}
	return c, nil
}

func isTextMime(mime string) bool {
	return tool.IsTextMime(mime)
}

func (r *Runtime) ensureSession(ctx context.Context, convID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.known[convID]; ok {
		return nil
	}
	_, err := r.sessions.Create(ctx, &session.CreateRequest{
		AppName:   r.appName,
		UserID:    convID,
		SessionID: convID,
	})
	if err != nil {
		return fmt.Errorf("create session %q: %w", convID, err)
	}
	r.known[convID] = struct{}{}
	return nil
}

// recorderAfterTool builds an ADK AfterToolCallback that snapshots each
// tool call and forwards it to the recorder. It returns nil to leave the
// tool's real result untouched. Fires on success and failure alike (ADK
// still calls after_tool when before_tool vetoes; result is empty, err set).
func recorderAfterTool(rec ToolRecorder) llmagent.AfterToolCallback {
	return func(tctx adktool.Context, t adktool.Tool, args, result map[string]any, inErr error) (map[string]any, error) {
		errMsg := ""
		if inErr != nil {
			errMsg = inErr.Error()
		}
		rec(ToolCall{
			Name:   t.Name(),
			Args:   copyArgs(args),
			Result: copyArgs(result),
			ErrMsg: errMsg,
		})
		return nil, nil
	}
}

func copyArgs(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
