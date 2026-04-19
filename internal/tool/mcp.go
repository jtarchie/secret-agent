package tool

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/session"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"

	"github.com/jtarchie/secret-agent/internal/bot"
)

// NewMCP builds an ADK Toolset that connects to the MCP server described by
// m. The transport is selected by which field of m is set: Command dispatches
// a stdio subprocess, URL connects over streamable HTTP. Connection is lazy —
// the server is not dialed until the agent asks for tools for the first time.
func NewMCP(m bot.MCPServer) (adktool.Toolset, error) {
	var transport mcp.Transport

	switch {
	case m.Command != "":
		cmd := exec.Command(m.Command, m.Args...)
		if len(m.Env) > 0 {
			cmd.Env = append(os.Environ(), envSlice(m.Env)...)
		}
		transport = &mcp.CommandTransport{Command: cmd}

	case m.URL != "":
		transport = &mcp.StreamableClientTransport{
			Endpoint:   m.URL,
			HTTPClient: newHeaderHTTPClient(m.Headers),
		}

	default:
		return nil, fmt.Errorf("mcp %q: no transport (command or url) set", m.Name)
	}

	cfg := mcptoolset.Config{
		Transport:           transport,
		RequireConfirmation: m.RequireConfirmation,
	}

	ts, err := mcptoolset.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %w", m.Name, err)
	}
	if len(m.ToolFilter) > 0 {
		ts = adktool.FilterToolset(ts, adktool.AllowedToolsPredicate(m.ToolFilter))
	}
	return ts, nil
}

// envSlice converts a map into the KEY=VALUE form expected by exec.Cmd.Env.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// newHeaderHTTPClient returns an *http.Client that injects the given headers
// on every request. A nil or empty map yields http.DefaultClient.
func newHeaderHTTPClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &headerRoundTripper{
			headers: headers,
			base:    http.DefaultTransport,
		},
	}
}

type headerRoundTripper struct {
	headers map[string]string
	base    http.RoundTripper
}

// PreflightMCP probes an MCP toolset by listing its tools, returning any
// connect or handshake error. It reuses the toolset's cached session so a
// successful probe warms up the first real chat turn.
func PreflightMCP(ctx context.Context, ts adktool.Toolset) error {
	_, err := ts.Tools(preflightCtx{Context: ctx})
	if err != nil {
		return fmt.Errorf("mcp tools: %w", err)
	}
	return nil
}

// preflightCtx is the minimal agent.ReadonlyContext needed to satisfy
// Toolset.Tools during startup probes. Only the embedded context.Context is
// meaningful — the remaining fields return zero values because mcptoolset
// only uses the context for cancellation/timeout propagation.
type preflightCtx struct {
	context.Context //nolint:containedctx // ADK's ReadonlyContext interface embeds context; this is the only legal shape
}

var _ agent.ReadonlyContext = preflightCtx{}

func (preflightCtx) UserContent() *genai.Content          { return nil }
func (preflightCtx) InvocationID() string                 { return "" }
func (preflightCtx) AgentName() string                    { return "" }
func (preflightCtx) ReadonlyState() session.ReadonlyState { return emptyState{} }
func (preflightCtx) UserID() string                       { return "" }
func (preflightCtx) AppName() string                      { return "" }
func (preflightCtx) SessionID() string                    { return "" }
func (preflightCtx) Branch() string                       { return "" }

type emptyState struct{}

func (emptyState) Get(string) (any, error) { return nil, session.ErrStateKeyNotExist }
func (emptyState) All() iter.Seq2[string, any] {
	return func(func(string, any) bool) {}
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range h.headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := h.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("round trip: %w", err)
	}
	return resp, nil
}
