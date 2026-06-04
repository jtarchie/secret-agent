package mcpauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

// RuntimeHandler implements auth.OAuthHandler for the non-interactive run /
// once path. It loads tokens from the Store and refreshes silently. If a
// server returns 401 (meaning even a refreshed token wasn't accepted), it
// surfaces an actionable error telling the user to re-run `mcp login`.
type RuntimeHandler struct {
	store   *Store
	botName string
	mcpName string
}

// NewRuntimeHandler builds a handler that pulls credentials for (botName,
// mcpName) from store. store must be non-nil.
func NewRuntimeHandler(store *Store, botName, mcpName string) *RuntimeHandler {
	return &RuntimeHandler{store: store, botName: botName, mcpName: mcpName}
}

var _ auth.OAuthHandler = (*RuntimeHandler)(nil)

// TokenSource returns a refreshing oauth2.TokenSource backed by the store.
// On refresh, the new token is written back to the store so subsequent
// invocations don't redo the refresh.
//
// When no entry is persisted, TokenSource returns (nil, nil) per the
// auth.OAuthHandler contract: the transport will then send the request
// without an Authorization header. If the server is open, or already
// authenticated via static headers (mcp[].headers), the request succeeds.
// If the server requires OAuth, it returns 401 and the transport calls
// Authorize, which surfaces the actionable reauth hint.
func (h *RuntimeHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	entry, ok := h.store.Get(Key(h.botName, h.mcpName))
	if !ok || entry.Token == nil {
		return nil, nil
	}

	cfg := configFromEntry(entry)
	seedTok := *entry.Token
	base := cfg.TokenSource(ctx, entry.Token)
	return &notifyingTokenSource{
		base:    base,
		store:   h.store,
		key:     Key(h.botName, h.mcpName),
		seed:    *entry,
		lastTok: &seedTok,
	}, nil
}

// Authorize is invoked when the transport sees a 401/403. In runtime mode we
// don't pop a browser; we surface a clear error so preflight (or the live
// session) fails with an actionable hint.
func (h *RuntimeHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return fmt.Errorf("mcp %q on bot %q needs reauthorization; run: secret-agent mcp login <bot.yml> --mcp %s",
		h.mcpName, h.botName, h.mcpName)
}

// configFromEntry builds an oauth2.Config sufficient for refreshing. The
// Endpoint includes AuthStyle so refresh requests use the right client-auth
// method (basic vs. form-encoded).
func configFromEntry(e *Entry) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     e.ClientID,
		ClientSecret: e.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   e.AuthURL,
			TokenURL:  e.TokenURL,
			AuthStyle: e.AuthStyle,
		},
		Scopes: e.Scopes,
	}
}

// notifyingTokenSource wraps an oauth2.TokenSource and persists any refreshed
// token back to the Store. golang.org/x/oauth2 caches refreshes in memory
// only; without this wrapper the new access token would be lost on restart.
type notifyingTokenSource struct {
	base  oauth2.TokenSource
	store *Store
	key   string
	seed  Entry

	mu      sync.Mutex
	lastTok *oauth2.Token
}

// Token returns the current token, refreshing if needed. When the underlying
// source returns a token that differs from the last one we observed, we
// rewrite the store entry. The persistence error is non-fatal: a refreshed
// token in memory is still usable for the current process; we only lose the
// optimization on restart.
func (n *notifyingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := n.base.Token()
	if err != nil {
		return nil, fmt.Errorf("oauth2 token: %w", err)
	}

	n.mu.Lock()
	changed := n.lastTok == nil ||
		n.lastTok.AccessToken != tok.AccessToken ||
		n.lastTok.RefreshToken != tok.RefreshToken ||
		!n.lastTok.Expiry.Equal(tok.Expiry)
	if changed {
		n.lastTok = tok
	}
	n.mu.Unlock()

	if changed {
		updated := n.seed
		updated.Token = tok
		_ = n.store.Put(n.key, &updated)
	}
	return tok, nil
}
