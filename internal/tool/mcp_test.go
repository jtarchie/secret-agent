package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"

	"github.com/jtarchie/secret-agent/internal/bot"
)

func TestNewMCPStdio(t *testing.T) {
	ts, err := NewMCP(bot.MCPServer{
		Name:    "fs",
		Command: "echo",
		Args:    []string{"hello"},
	})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	if ts == nil {
		t.Fatal("got nil toolset")
	}
}

func TestNewMCPURL(t *testing.T) {
	ts, err := NewMCP(bot.MCPServer{
		Name: "maps",
		URL:  "https://example.com/mcp",
		Headers: map[string]string{
			"Authorization": "Bearer token",
		},
	})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	if ts == nil {
		t.Fatal("got nil toolset")
	}
}

func TestNewMCPNoTransport(t *testing.T) {
	_, err := NewMCP(bot.MCPServer{Name: "empty"})
	if err == nil {
		t.Fatal("expected error when no transport set")
	}
}

func TestHeaderHTTPClientInjectsHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newHeaderHTTPClient(map[string]string{
		"Authorization": "Bearer secret",
		"X-Custom":      "yep",
	})

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret")
	}
	if gotCustom != "yep" {
		t.Errorf("X-Custom = %q, want %q", gotCustom, "yep")
	}
}

func TestHeaderHTTPClientEmptyMapReturnsDefault(t *testing.T) {
	if newHeaderHTTPClient(nil) != http.DefaultClient {
		t.Error("nil headers should return http.DefaultClient")
	}
	if newHeaderHTTPClient(map[string]string{}) != http.DefaultClient {
		t.Error("empty headers should return http.DefaultClient")
	}
}

func TestPreflightMCPSuccess(t *testing.T) {
	clientT, serverT := mcp.NewInMemoryTransports()

	srv := mcp.NewServer(&mcp.Implementation{Name: "preflight-srv", Version: "0.0.1"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping"}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = srv.Run(ctx, serverT) }()

	ts, err := mcptoolset.New(mcptoolset.Config{Transport: clientT})
	if err != nil {
		t.Fatalf("mcptoolset.New: %v", err)
	}

	if err := PreflightMCP(ctx, ts); err != nil {
		t.Fatalf("PreflightMCP: %v", err)
	}
}

// blockingToolset is a context-respecting tool.Toolset stand-in used to
// verify PreflightMCP honors its context deadline without relying on the
// MCP Go SDK's HTTP retry/close semantics.
type blockingToolset struct{}

func (blockingToolset) Name() string        { return "blocking" }
func (blockingToolset) Description() string { return "blocks until context is done" }
func (blockingToolset) IsLongRunning() bool { return false }
func (blockingToolset) Tools(ctx agent.ReadonlyContext) ([]adktool.Tool, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestPreflightMCPTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := PreflightMCP(ctx, blockingToolset{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("preflight took too long (%s); timeout should have fired at ~150ms", elapsed)
	}
	if elapsed < 150*time.Millisecond/2 {
		t.Errorf("preflight returned too early (%s)", elapsed)
	}
}

func TestHeaderHTTPClientPreservesExistingHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newHeaderHTTPClient(map[string]string{
		"Authorization": "Bearer default",
	})

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer caller")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if got != "Bearer caller" {
		t.Errorf("Authorization = %q, want caller's value to win", got)
	}
}
