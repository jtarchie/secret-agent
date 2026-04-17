// Package runtime wires a Bot into an ADK runner and exposes a simple Send API.
package runtime

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/jtarchie/secret-agent/internal/bot"
)

type Runtime struct {
	appName   string
	userID    string
	sessionID string
	runner    *runner.Runner
}

func New(ctx context.Context, b *bot.Bot, llm adkmodel.LLM) (*Runtime, error) {
	root, err := llmagent.New(llmagent.Config{
		Name:        b.Name,
		Description: fmt.Sprintf("YAML-defined bot %q", b.Name),
		Model:       llm,
		Instruction: b.System,
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

// Send runs one turn and returns the bot's final text response.
func (r *Runtime) Send(ctx context.Context, userMsg string) (string, error) {
	msg := genai.NewContentFromText(userMsg, genai.RoleUser)

	var reply strings.Builder
	for ev, err := range r.runner.Run(ctx, r.userID, r.sessionID, msg, agent.RunConfig{}) {
		if err != nil {
			return "", err
		}
		if !ev.IsFinalResponse() || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.Text != "" {
				reply.WriteString(p.Text)
			}
		}
	}
	return reply.String(), nil
}
