package mcpauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLoginEndToEnd stands up a stub MCP host that serves protected-resource
// metadata, authorization-server metadata, dynamic client registration, an
// authorization endpoint that immediately redirects back with a code, and a
// token endpoint. Asserts that Login persists a complete Entry.
func TestLoginEndToEnd(t *testing.T) {
	var (
		registered atomic.Bool
		exchanged  atomic.Bool
	)

	// We use a single httptest.Server as both the resource and the
	// authorization server. The MCP "resource" URL points at /mcp; the AS
	// lives at the server root.
	var srv *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              srv.URL + "/mcp",
			"authorization_servers": []string{srv.URL},
			"scopes_supported":      []string{"read", "write"},
		})
	})

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"registration_endpoint":  srv.URL + "/register",
			"response_types_supported": []string{"code"},
			"jwks_uri":               srv.URL + "/jwks",
			"code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		registered.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":                  "cid-xyz",
			"redirect_uris":              []string{},
			"token_endpoint_auth_method": "none",
		})
	})

	// /authorize redirects straight back to the callback with a known code,
	// echoing the state. The stub doesn't validate PKCE on /authorize; that
	// only matters at /token.
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		redirect := q.Get("redirect_uri")
		state := q.Get("state")
		u, err := url.Parse(redirect)
		if err != nil {
			http.Error(w, "bad redirect_uri", 400)
			return
		}
		qq := u.Query()
		qq.Set("code", "code-abc")
		qq.Set("state", state)
		u.RawQuery = qq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		exchanged.Store(true)
		err := r.ParseForm()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// Bare minimum sanity: PKCE verifier was sent on the exchange.
		if r.Form.Get("code_verifier") == "" {
			http.Error(w, "missing code_verifier", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "atk-1",
			"refresh_token": "rtk-1",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})

	srv = httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	store, err := OpenAt(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}

	// Drive the browser step: as soon as Login writes the URL to Out, fetch
	// the authorize URL with the stub's redirect client. This stands in for
	// the user clicking through consent.
	var out bytes.Buffer
	openCh := make(chan string, 1)
	openFn := func(u string) error {
		openCh <- u
		return nil
	}

	doneCh := make(chan error, 1)
	go func() {
		_, lerr := Login(context.Background(), store, LoginConfig{
			BotName: "mybot",
			MCPName: "stub",
			MCPURL:  srv.URL + "/mcp",
			Listen:  "127.0.0.1:0",
			Out:     &out,
			OpenURL: openFn,
			Timeout: 10 * time.Second,
		})
		doneCh <- lerr
	}()

	select {
	case authURL := <-openCh:
		// Visit the authorize URL using an HTTP client that follows the
		// redirect back to our local callback listener. The default client
		// follows redirects, which calls into the loopback callback handler
		// and drives the rest of the flow.
		resp, herr := http.Get(authURL)
		if herr != nil {
			t.Fatalf("simulated browser GET: %v", herr)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("Login never opened the auth URL")
	}

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Login did not return after callback")
	}

	if !registered.Load() {
		t.Error("DCR endpoint was never called")
	}
	if !exchanged.Load() {
		t.Error("token endpoint was never called")
	}

	got, ok := store.Get(Key("mybot", "stub"))
	if !ok {
		t.Fatal("no entry persisted")
	}
	if got.Token.AccessToken != "atk-1" || got.Token.RefreshToken != "rtk-1" {
		t.Errorf("token mismatch: %+v", got.Token)
	}
	if got.ClientID != "cid-xyz" {
		t.Errorf("ClientID = %q want cid-xyz", got.ClientID)
	}
	if got.AuthURL != srv.URL+"/authorize" || got.TokenURL != srv.URL+"/token" {
		t.Errorf("endpoints mismatch: %+v", got)
	}
	if got.Resource != srv.URL+"/mcp" {
		t.Errorf("Resource = %q want %s/mcp", got.Resource, srv.URL)
	}
	if !strings.Contains(out.String(), "Stored credentials") {
		t.Errorf("Out did not log success path: %q", out.String())
	}
}
