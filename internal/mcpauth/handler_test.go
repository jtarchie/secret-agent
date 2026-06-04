package mcpauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestRuntimeHandlerNoEntryReturnsNilSource(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	h := NewRuntimeHandler(s, "mybot", "linear")

	ts, err := h.TokenSource(context.Background())
	if err != nil {
		t.Errorf("TokenSource err = %v; want nil so the SDK skips the Authorization header", err)
	}
	if ts != nil {
		t.Error("TokenSource source should be nil when no entry is persisted")
	}
}

func TestRuntimeHandlerAuthorizeAlwaysErrors(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	h := NewRuntimeHandler(s, "b", "m")
	resp := &http.Response{StatusCode: 401, Body: http.NoBody}
	req := &http.Request{}
	err = h.Authorize(context.Background(), req, resp)
	if err == nil {
		t.Fatal("Authorize must error in runtime mode")
	}
	if !strings.Contains(err.Error(), "mcp login") {
		t.Errorf("error should hint at `mcp login`; got %v", err)
	}
}

func TestRuntimeHandlerRefreshWritesBackToStore(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		err := r.ParseForm()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			http.Error(w, "expected refresh_token grant", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	s, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	tokenURL, _ := url.Parse(srv.URL + "/token")

	err = s.Put(Key("bot", "srv"), &Entry{
		Token: &oauth2.Token{
			AccessToken:  "old-access",
			RefreshToken: "rtk",
			Expiry:       time.Now().Add(-time.Hour),
			TokenType:    "Bearer",
		},
		ClientID:  "cid",
		AuthURL:   srv.URL + "/authorize",
		TokenURL:  tokenURL.String(),
		AuthStyle: oauth2.AuthStyleInParams,
		Scopes:    []string{"read"},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	h := NewRuntimeHandler(s, "bot", "srv")
	ts, err := h.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q want new-access", tok.AccessToken)
	}

	// Reload the store from disk and confirm the new token is persisted.
	s2, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt reload: %v", err)
	}
	got, ok := s2.Get(Key("bot", "srv"))
	if !ok {
		t.Fatal("entry missing after refresh")
	}
	if got.Token.AccessToken != "new-access" {
		t.Errorf("persisted AccessToken = %q want new-access", got.Token.AccessToken)
	}
	if got.Token.RefreshToken != "new-refresh" {
		t.Errorf("persisted RefreshToken = %q want new-refresh", got.Token.RefreshToken)
	}
	// Client metadata must survive the refresh write.
	if got.ClientID != "cid" || got.AuthURL != srv.URL+"/authorize" {
		t.Errorf("seed metadata lost on refresh: %+v", got)
	}
}

func TestRuntimeHandlerNoRefreshNoWrite(t *testing.T) {
	// If the cached token is still valid, the oauth2 source returns it
	// without hitting the token endpoint, and we must not rewrite the store.
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	s, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}

	original := &Entry{
		Token: &oauth2.Token{
			AccessToken:  "still-good",
			RefreshToken: "rtk",
			Expiry:       time.Now().Add(time.Hour),
			TokenType:    "Bearer",
		},
		ClientID: "cid",
		AuthURL:  "https://auth/authorize",
		TokenURL: "https://auth/token",
	}
	err = s.Put(Key("bot", "srv"), original)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	h := NewRuntimeHandler(s, "bot", "srv")
	ts, err := h.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.AccessToken != "still-good" {
		t.Errorf("AccessToken = %q want still-good", tok.AccessToken)
	}

	st2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat2: %v", err)
	}
	if !st.ModTime().Equal(st2.ModTime()) {
		t.Errorf("store was rewritten despite no refresh: %v -> %v", st.ModTime(), st2.ModTime())
	}
}
