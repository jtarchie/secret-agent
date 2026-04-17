// Package runtime wires a Bot into an ADK runner and exposes a simple Send API.
package runtime

import (
	"context"
	"fmt"

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
	appName   string
	userID    string
	sessionID string
	runner    *runner.Runner
}

func New(ctx context.Context, b *bot.Bot, llm adkmodel.LLM) (*Runtime, error) {
	tools := make([]adktool.Tool, 0, len(b.Tools))
	for _, t := range b.Tools {
		built, err := tool.NewShell(t.Name, t.Description, t.Sh, t.Params)
		if err != nil {
			return nil, fmt.Errorf("tool %q: %w", t.Name, err)
		}
		tools = append(tools, built)
	}

	root, err := llmagent.New(llmagent.Config{
		Name:        b.Name,
		Description: fmt.Sprintf("YAML-defined bot %q", b.Name),
		Model:       llm,
		Instruction: b.System,
		Tools:       tools,
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	sessions := session.InMemoryService()
	const userID, sessionID = "local", "local"

	if _, err := sessions.Create(ctx, &session.CreateRequest{
		AppName:   b.Name,
		UserID:    userID,
		SessionID: sessionID,
	}); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

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
		userID:    userID,
		sessionID: sessionID,
		runner:    r,
	}, nil
}

// Send runs one turn and streams the bot's reply as chat.Chunks.
// The returned channel is closed when the turn completes.
func (r *Runtime) Send(ctx context.Context, userMsg string) <-chan chat.Chunk {
	out := make(chan chat.Chunk)

	go func() {
		defer close(out)

		msg := genai.NewContentFromText(userMsg, genai.RoleUser)
		emit := func(c chat.Chunk) bool {
			select {
			case out <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for ev, err := range r.runner.Run(ctx, r.userID, r.sessionID, msg, agent.RunConfig{}) {
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
