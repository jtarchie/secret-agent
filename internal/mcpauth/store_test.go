package mcpauth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestOpenAtMissing(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if _, ok := s.Get("anything"); ok {
		t.Fatal("expected empty store")
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	s, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}

	want := &Entry{
		Token: &oauth2.Token{
			AccessToken:  "atk",
			RefreshToken: "rtk",
			Expiry:       time.Now().Add(time.Hour).UTC().Truncate(time.Second),
			TokenType:    "Bearer",
		},
		ClientID:     "cid",
		ClientSecret: "csec",
		AuthURL:      "https://auth/auth",
		TokenURL:     "https://auth/token",
		AuthStyle:    oauth2.AuthStyleInParams,
		Scopes:       []string{"a", "b"},
		Resource:     "https://srv/mcp",
	}
	err = s.Put(Key("bot", "srv"), want)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	s2, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt reload: %v", err)
	}
	got, ok := s2.Get(Key("bot", "srv"))
	if !ok {
		t.Fatal("entry missing after reload")
	}
	if got.Token.AccessToken != want.Token.AccessToken {
		t.Errorf("AccessToken = %q want %q", got.Token.AccessToken, want.Token.AccessToken)
	}
	if got.Token.RefreshToken != want.Token.RefreshToken {
		t.Errorf("RefreshToken = %q want %q", got.Token.RefreshToken, want.Token.RefreshToken)
	}
	if got.ClientID != want.ClientID || got.ClientSecret != want.ClientSecret {
		t.Errorf("client creds = (%q, %q) want (%q, %q)", got.ClientID, got.ClientSecret, want.ClientID, want.ClientSecret)
	}
	if got.AuthURL != want.AuthURL || got.TokenURL != want.TokenURL {
		t.Errorf("endpoints mismatch: %+v", got)
	}
	if got.AuthStyle != want.AuthStyle {
		t.Errorf("AuthStyle = %v want %v", got.AuthStyle, want.AuthStyle)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "a" {
		t.Errorf("Scopes = %v", got.Scopes)
	}
	if got.Resource != want.Resource {
		t.Errorf("Resource = %q want %q", got.Resource, want.Resource)
	}
	if got.IssuedAt.IsZero() {
		t.Error("IssuedAt unset")
	}
}

func TestPutWritesRestrictivePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")

	s, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	err = s.Put(Key("bot", "srv"), &Entry{ClientID: "cid"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Mode().Perm() != tokenFilMod {
		t.Errorf("file mode = %o, want %o", st.Mode().Perm(), tokenFilMod)
	}
}

func TestGetReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenAt(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	err = s.Put(Key("b", "s"), &Entry{
		Token:    &oauth2.Token{AccessToken: "atk"},
		ClientID: "cid",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, _ := s.Get(Key("b", "s"))
	got.Token.AccessToken = "mutated"
	got.ClientID = "mutated"

	fresh, _ := s.Get(Key("b", "s"))
	if fresh.Token.AccessToken != "atk" || fresh.ClientID != "cid" {
		t.Errorf("Get returned aliased entry; mutation leaked: token=%q clientID=%q", fresh.Token.AccessToken, fresh.ClientID)
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	s, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	err = s.Put(Key("b", "s"), &Entry{ClientID: "cid"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	err = s.Delete(Key("b", "s"))
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get(Key("b", "s")); ok {
		t.Error("entry still present after delete")
	}
	// Persisted: reopen sees no entry.
	s2, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt reload: %v", err)
	}
	if _, ok := s2.Get(Key("b", "s")); ok {
		t.Error("entry survived delete after reload")
	}
}
