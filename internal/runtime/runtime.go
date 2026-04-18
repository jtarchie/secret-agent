// Package runtime wires a Bot into an ADK runner and exposes a simple Send API.
package runtime

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/tool"
)

type Runtime struct {
	appName  string
	sessions session.Service
	runner   *runner.Runner

	mu    sync.Mutex
	known map[string]struct{}
}

func New(ctx context.Context, b *bot.Bot, llm adkmodel.LLM) (*Runtime, error) {
	root, err := buildAgent(b.Name, fmt.Sprintf("YAML-defined bot %q", b.Name), b, llm)
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
		appName:  b.Name,
		sessions: sessions,
		runner:   r,
		known:    map[string]struct{}{},
	}, nil
}

// buildAgent constructs an ADK llmagent for a bot, recursively wrapping each
// declared sub-agent as an AgentTool so the parent LLM can invoke it by name.
// The provided name/description override the bot's own so parent-defined map
// keys and descriptions drive what the parent LLM sees.
func buildAgent(name, description string, b *bot.Bot, llm adkmodel.LLM) (agent.Agent, error) {
	tools := make([]adktool.Tool, 0, len(b.Tools)+len(b.Agents))
	for _, t := range b.Tools {
		var (
			built adktool.Tool
			err   error
		)
		switch {
		case t.Sh != "":
			built, err = tool.NewShell(t.Name, t.Description, t.Sh, t.Params)
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
		child, err := buildAgent(key, childDesc, ref.Bot, llm)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", key, err)
		}
		wrapped, err := tool.NewSubAgent(key, childDesc, child, ref.SkipSummarization, ref.Attachments)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", key, err)
		}
		tools = append(tools, wrapped)
	}

	built, err := llmagent.New(llmagent.Config{
		Name:        name,
		Description: description,
		Model:       llm,
		Instruction: b.System,
		Tools:       tools,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent %q: %w", name, err)
	}
	return built, nil
}

// HandlerFor returns a chat.Handler bound to the given conversation ID.
// The underlying ADK session is created lazily (in-memory) on first use.
func (r *Runtime) HandlerFor(convID string) chat.Handler {
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

			if err := r.ensureSession(ctx, convID); err != nil {
				emit(chat.Chunk{Err: err})
				return
			}

			msg, err := buildUserContent(userMsg)
			if err != nil {
				emit(chat.Chunk{Err: err})
				return
			}
			runCtx := tool.WithAttachments(ctx, userMsg.Attachments)
			for ev, err := range r.runner.Run(runCtx, convID, convID, msg, agent.RunConfig{}) {
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
	return tool.BuildAttachedContent(msg.Text, msg.Attachments)
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
	if _, err := r.sessions.Create(ctx, &session.CreateRequest{
		AppName:   r.appName,
		UserID:    convID,
		SessionID: convID,
	}); err != nil {
		return fmt.Errorf("create session %q: %w", convID, err)
	}
	r.known[convID] = struct{}{}
	return nil
}
