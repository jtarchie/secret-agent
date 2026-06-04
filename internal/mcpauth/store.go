// Package mcpauth persists OAuth credentials and tokens for HTTP MCP servers
// configured in a bot YAML, and exposes runtime/login OAuth handlers that
// drive the MCP Go SDK's auth flow.
//
// State is stored as a single JSON file under the user's config directory
// (typically ~/.config/secret-agent/mcp-tokens.json on Linux,
// ~/Library/Application Support/secret-agent/mcp-tokens.json on macOS),
// with parent dir perms 0700 and file perms 0600. Each agent's MCP server
// gets a record keyed by "<botName>:<mcpName>".
package mcpauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	// AppDirName is the directory created under os.UserConfigDir() to hold
	// secret-agent state. Exported so other internal packages can stay in sync.
	AppDirName  = "secret-agent"
	tokensFile  = "mcp-tokens.json"
	tokenDirMod = 0o700
	tokenFilMod = 0o600
)

// Entry is one persisted OAuth record for a single MCP server.
type Entry struct {
	// Token holds the most recently issued access token, plus its refresh
	// token and expiry. Refreshed tokens are written back automatically by
	// notifyingTokenSource.
	Token *oauth2.Token `json:"token"`

	// ClientID/ClientSecret are the client credentials issued by the
	// authorization server (via DCR or preregistration). Needed to refresh.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`

	// AuthURL and TokenURL are cached so refreshes don't re-discover.
	AuthURL  string `json:"auth_url"`
	TokenURL string `json:"token_url"`

	// AuthStyle controls whether client credentials go in the POST body or
	// the Authorization header on refresh.
	AuthStyle oauth2.AuthStyle `json:"auth_style"`

	// Scopes granted at authorization time. Re-sent on refresh for servers
	// that require it.
	Scopes []string `json:"scopes,omitempty"`

	// Resource is the MCP server URL the token was issued for. Echoed on
	// refresh as the "resource" parameter for servers that bind tokens
	// per-resource (MCP spec, RFC 8707).
	Resource string `json:"resource,omitempty"`

	// IssuedAt is the wall-clock time the record was written. Informational.
	IssuedAt time.Time `json:"issued_at"`
}

// Key returns the JSON map key for a (bot, mcp-server) pair.
func Key(botName, mcpName string) string { return botName + ":" + mcpName }

// Store is a thread-safe JSON-backed map of Entry records.
type Store struct {
	path string

	mu      sync.Mutex
	entries map[string]*Entry
}

// Open loads (or creates) the store at the user's default location. If the
// file doesn't exist yet, Open returns an empty Store; the file is created on
// the first Put. If the file is present, its permissions are tightened to
// 0600 on open.
func Open() (*Store, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return OpenAt(filepath.Join(dir, tokensFile))
}

// OpenAt loads the store from a specific path. Useful in tests.
func OpenAt(path string) (*Store, error) {
	s := &Store{path: path, entries: map[string]*Entry{}}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	if len(raw) == 0 {
		return s, nil
	}
	err = json.Unmarshal(raw, &s.entries)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	// Tighten perms on existing file in case it was created by an older
	// version with a looser umask.
	_ = os.Chmod(path, tokenFilMod)
	return s, nil
}

// DefaultDir returns the directory the store file lives in. The directory is
// created (mode 0700) if it doesn't exist.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("user config dir: %w", err)
	}
	dir := filepath.Join(base, AppDirName)
	err = os.MkdirAll(dir, tokenDirMod)
	if err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return dir, nil
}

// Path returns the on-disk path the store is backed by.
func (s *Store) Path() string { return s.path }

// Get returns the entry for key, if present.
func (s *Store) Get(key string) (*Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	cp := *e
	if e.Token != nil {
		tok := *e.Token
		cp.Token = &tok
	}
	return &cp, true
}

// Put stores entry under key and flushes the whole store to disk atomically.
func (s *Store) Put(key string, entry *Entry) error {
	if entry == nil {
		return errors.New("mcpauth: nil entry")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *entry
	if entry.Token != nil {
		tok := *entry.Token
		cp.Token = &tok
	}
	if cp.IssuedAt.IsZero() {
		cp.IssuedAt = time.Now().UTC()
	}
	s.entries[key] = &cp
	return s.flushLocked()
}

// Delete removes the entry for key and flushes the store. No-op if absent.
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[key]
	if !ok {
		return nil
	}
	delete(s.entries, key)
	return s.flushLocked()
}

// flushLocked writes the current entries map to disk via a temp file +
// rename so a partial write can't corrupt the store. Caller must hold s.mu.
func (s *Store) flushLocked() error {
	// Ensure the parent dir exists with tight perms (e.g. after a wipe).
	err := os.MkdirAll(filepath.Dir(s.path), tokenDirMod)
	if err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(s.path), err)
	}

	raw, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encode entries: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), tokensFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	err = tmp.Chmod(tokenFilMod)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}

	_, err = tmp.Write(raw)
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	err = tmp.Close()
	if err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpName, err)
	}

	err = os.Rename(tmpName, s.path)
	if err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmpName, s.path, err)
	}
	return nil
}
