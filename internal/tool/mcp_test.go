package tool

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
