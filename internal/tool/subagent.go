package tool

import (
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/artifact"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// SubAgentResult is the JSON-shaped output the child's final text answer is
// wrapped in when returned to the parent LLM.
type SubAgentResult struct {
	Result string `json:"result"`
}

// NewSubAgent wraps a child ADK agent as a tool the parent LLM can call.
// Unlike ADK's stock agenttool.New (which drops non-text parts at the call
// boundary), this can forward attachments to the child when
// forwardAttachments is true: the parent LLM then sees an optional
// `attachments` parameter it can populate with index/filename references
// into the current turn, and those files are packed into the child's
// genai.Content (text inlined; binary as InlineData). When
// forwardAttachments is false, the parameter is not declared and any value
// supplied for it is ignored — the child is strictly isolated from the
// parent turn's attachments.
func NewSubAgent(name, description string, child agent.Agent, skipSummarization, forwardAttachments bool) (adktool.Tool, error) {
	props := map[string]*jsonschema.Schema{
		"request": {
			Type:        "string",
			Description: "The natural-language instruction or question to send to the sub-agent.",
		},
	}
	if forwardAttachments {
		props["attachments"] = &jsonschema.Schema{
			Type:        "array",
			Description: "Optional: list of current-turn attachment references (index like \"0\" or filename) to forward to the sub-agent.",
			Items:       &jsonschema.Schema{Type: "string"},
		}
	}
	schema := &jsonschema.Schema{
		Type:       "object",
		Properties: props,
		Required:   []string{"request"},
	}

	return functiontool.New(
		functiontool.Config{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
		func(ctx adktool.Context, args map[string]any) (SubAgentResult, error) {
			request, _ := args["request"].(string)
			request = strings.TrimSpace(request)
			if request == "" {
				return SubAgentResult{}, fmt.Errorf("request is required")
			}

			var forwarded []chat.Attachment
			if forwardAttachments {
				refs, _ := args["attachments"].([]any)
				if len(refs) > 0 {
					parentAtts := AttachmentsFromContext(ctx)
					for _, raw := range refs {
						ref := fmt.Sprintf("%v", raw)
						path, err := resolveAttachment(ref, parentAtts)
						if err != nil {
							return SubAgentResult{}, fmt.Errorf("attachment %q: %w", ref, err)
						}
						for _, a := range parentAtts {
							if a.Path == path {
								forwarded = append(forwarded, a)
								break
							}
						}
					}
				}
			}

			content, err := BuildAttachedContent(request, forwarded)
			if err != nil {
				return SubAgentResult{}, fmt.Errorf("build sub-agent content: %w", err)
			}

			if skipSummarization {
				if actions := ctx.Actions(); actions != nil {
					actions.SkipSummarization = true
				}
			}

			sessionService := session.InMemoryService()
			r, err := runner.New(runner.Config{
				AppName:         child.Name(),
				Agent:           child,
				SessionService:  sessionService,
				ArtifactService: artifact.InMemoryService(),
				MemoryService:   memory.InMemoryService(),
			})
			if err != nil {
				return SubAgentResult{}, fmt.Errorf("create sub-agent runner: %w", err)
			}

			userID := ctx.UserID()
			if userID == "" {
				userID = "sub-agent"
			}
			subSession, err := sessionService.Create(ctx, &session.CreateRequest{
				AppName: child.Name(),
				UserID:  userID,
			})
			if err != nil {
				return SubAgentResult{}, fmt.Errorf("create sub-agent session: %w", err)
			}

			runCtx := WithAttachments(ctx, forwarded)

			var lastEvent *session.Event
			for event, err := range r.Run(runCtx, subSession.Session.UserID(), subSession.Session.ID(), content, agent.RunConfig{
				StreamingMode: agent.StreamingModeSSE,
			}) {
				if err != nil {
					return SubAgentResult{}, fmt.Errorf("sub-agent %q: %w", child.Name(), err)
				}
				if event.ErrorCode != "" || event.ErrorMessage != "" {
					return SubAgentResult{}, fmt.Errorf("sub-agent %q: %s: %s", child.Name(), event.ErrorCode, event.ErrorMessage)
				}
				if event.LLMResponse.Content != nil {
					lastEvent = event
				}
			}

			if lastEvent == nil {
				return SubAgentResult{}, nil
			}

			var textParts []string
			for _, part := range lastEvent.LLMResponse.Content.Parts {
				if part != nil && part.Text != "" {
					textParts = append(textParts, part.Text)
				}
			}
			return SubAgentResult{Result: strings.Join(textParts, "\n")}, nil
		},
	)
}
