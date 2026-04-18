// Package runtime wires a Bot into an ADK runner and exposes a simple Send API.
package runtime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
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

// buildUserContent composes a user-role genai.Content from a chat.Message.
// A manifest block is prepended to the text so the model can reference
// attachments by index or filename when calling tools. Text-typed
// attachments are inlined into the text part (broadly compatible with
// OpenAI-compatible servers that only accept "text" and "image_url" content
// types). Binary attachments ride along as genai InlineData parts.
func buildUserContent(msg chat.Message) (*genai.Content, error) {
	if len(msg.Attachments) == 0 {
		return genai.NewContentFromText(msg.Text, genai.RoleUser), nil
	}

	type loaded struct {
		a      chat.Attachment
		data   []byte
		mime   string
		inline bool
	}
	items := make([]loaded, 0, len(msg.Attachments))
	for _, a := range msg.Attachments {
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", a.Path, err)
		}
		mime := a.ContentType
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		items = append(items, loaded{a: a, data: data, mime: mime, inline: isTextMime(mime)})
	}

	var buf strings.Builder
	buf.WriteString("<attachments>\n")
	for i, it := range items {
		name := it.a.Filename
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&buf, "- index=%d filename=%q type=%q\n", i, name, it.mime)
	}
	buf.WriteString("</attachments>")

	for i, it := range items {
		if !it.inline {
			continue
		}
		name := it.a.Filename
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&buf, "\n\n<attachment index=%d filename=%q>\n%s\n</attachment>", i, name, string(it.data))
	}

	if msg.Text != "" {
		buf.WriteString("\n\n")
		buf.WriteString(msg.Text)
	}

	parts := []*genai.Part{genai.NewPartFromText(buf.String())}
	for _, it := range items {
		if it.inline {
			continue
		}
		parts = append(parts, genai.NewPartFromBytes(it.data, it.mime))
	}
	return genai.NewContentFromParts(parts, genai.RoleUser), nil
}

// isTextMime reports whether a MIME type should be inlined as text rather
// than shipped as binary InlineData. We treat anything starting with "text/"
// plus common JSON / XML variants as text.
func isTextMime(mime string) bool {
	base, _, _ := strings.Cut(mime, ";")
	base = strings.TrimSpace(base)
	switch base {
	case "application/json", "application/xml", "application/yaml",
		"application/x-yaml", "application/javascript", "application/sh",
		"application/x-sh":
		return true
	}
	return strings.HasPrefix(base, "text/")
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
