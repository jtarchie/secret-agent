package tool

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"

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
	if len(m.ToolFilter) > 0 {
		cfg.ToolFilter = adktool.AllowedToolsPredicate(m.ToolFilter)
	}

	ts, err := mcptoolset.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %w", m.Name, err)
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

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range h.headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	return h.base.RoundTrip(req)
}
