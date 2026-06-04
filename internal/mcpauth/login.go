package mcpauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// LoginConfig parameterizes a single MCP server's interactive OAuth flow.
type LoginConfig struct {
	BotName   string
	MCPName   string
	MCPURL    string
	Listen    string // host:port for the OAuth callback; "" means 127.0.0.1:0.
	NoBrowser bool   // print the URL instead of opening it
	Out       io.Writer
	OpenURL   func(string) error // override for tests

	// HTTPClient is used for all discovery/registration/exchange requests.
	// When nil, http.DefaultClient is used.
	HTTPClient *http.Client

	// Scopes overrides the scopes derived from discovery. Optional.
	Scopes []string

	// Timeout bounds the total wait for the user to complete the browser
	// flow. Defaults to 5 minutes when zero.
	Timeout time.Duration
}

// Login runs the OAuth 2.1 authorization-code + PKCE flow against the MCP
// server described by cfg, opens the user's browser for consent, captures
// the callback, exchanges the code for a token, and persists the result to
// store under Key(cfg.BotName, cfg.MCPName).
//
// Returns the persisted Entry on success.
func Login(ctx context.Context, store *Store, cfg LoginConfig) (*Entry, error) {
	if store == nil {
		return nil, errors.New("mcpauth: nil store")
	}
	if cfg.MCPURL == "" {
		return nil, errors.New("mcpauth: empty MCP URL")
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.OpenURL == nil {
		cfg.OpenURL = openURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}

	// Bring up the loopback listener first so we know the exact port to
	// register as the redirect URI with the authorization server.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://%s/callback", ln.Addr().String())

	prm, err := discoverProtectedResource(ctx, cfg.HTTPClient, cfg.MCPURL)
	if err != nil {
		return nil, fmt.Errorf("discover protected resource: %w", err)
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, errors.New("protected resource metadata has no authorization_servers")
	}

	asm, err := auth.GetAuthServerMetadata(ctx, prm.AuthorizationServers[0], cfg.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("get authorization server metadata: %w", err)
	}
	if asm == nil {
		// Pre-2025-11-25 spec fallback: assume conventional endpoints under the
		// authorization server's root URL.
		root := prm.AuthorizationServers[0]
		asm = &oauthex.AuthServerMeta{
			Issuer:                root,
			AuthorizationEndpoint: root + "/authorize",
			TokenEndpoint:         root + "/token",
			RegistrationEndpoint:  root + "/register",
		}
	}
	if asm.RegistrationEndpoint == "" {
		return nil, errors.New("authorization server lacks registration_endpoint; preregistered clients are not yet supported")
	}

	regResp, err := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{redirectURI},
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		ClientName:              "secret-agent",
		ApplicationType:         "native",
	}, cfg.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("register client: %w", err)
	}

	authStyle := authMethodToStyle(regResp.TokenEndpointAuthMethod)
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = prm.ScopesSupported
	}

	oauthCfg := &oauth2.Config{
		ClientID:     regResp.ClientID,
		ClientSecret: regResp.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   asm.AuthorizationEndpoint,
			TokenURL:  asm.TokenEndpoint,
			AuthStyle: authStyle,
		},
		RedirectURL: redirectURI,
		Scopes:      scopes,
	}

	state, err := randomState()
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	codeVerifier := oauth2.GenerateVerifier()
	authURL := oauthCfg.AuthCodeURL(state,
		oauth2.S256ChallengeOption(codeVerifier),
		oauth2.SetAuthURLParam("resource", prm.Resource),
	)

	fmt.Fprintf(cfg.Out, "Authorizing MCP server %q for bot %q\n", cfg.MCPName, cfg.BotName)
	if cfg.NoBrowser {
		fmt.Fprintf(cfg.Out, "Open this URL in a browser to continue:\n  %s\n", authURL)
	} else {
		fmt.Fprintf(cfg.Out, "Opening browser; if it doesn't open, visit:\n  %s\n", authURL)
		err = cfg.OpenURL(authURL)
		if err != nil {
			fmt.Fprintf(cfg.Out, "  (could not open browser: %v)\n", err)
		}
	}

	code, err := awaitCallback(ctx, ln, state, cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("await callback: %w", err)
	}

	clientCtx := context.WithValue(ctx, oauth2.HTTPClient, cfg.HTTPClient)
	token, err := oauthCfg.Exchange(clientCtx, code,
		oauth2.VerifierOption(codeVerifier),
		oauth2.SetAuthURLParam("resource", prm.Resource),
	)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}

	entry := &Entry{
		Token:        token,
		ClientID:     regResp.ClientID,
		ClientSecret: regResp.ClientSecret,
		AuthURL:      asm.AuthorizationEndpoint,
		TokenURL:     asm.TokenEndpoint,
		AuthStyle:    authStyle,
		Scopes:       scopes,
		Resource:     prm.Resource,
	}
	err = store.Put(Key(cfg.BotName, cfg.MCPName), entry)
	if err != nil {
		return nil, fmt.Errorf("persist token: %w", err)
	}
	fmt.Fprintf(cfg.Out, "Stored credentials at %s\n", store.Path())
	return entry, nil
}

// discoverProtectedResource walks the MCP-spec well-known URL list for a
// resource. Returns the first successfully parsed PRM. Falls back to
// constructing a synthetic PRM that points at the MCP server root as the
// authorization server (pre-2025-11-25 spec).
func discoverProtectedResource(ctx context.Context, client *http.Client, mcpURL string) (*oauthex.ProtectedResourceMetadata, error) {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return nil, fmt.Errorf("parse mcp url: %w", err)
	}

	candidates := []struct {
		URL      string
		Resource string
	}{
		{
			URL:      (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/.well-known/oauth-protected-resource/" + strings.TrimLeft(u.Path, "/")}).String(),
			Resource: mcpURL,
		},
		{
			URL:      (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/.well-known/oauth-protected-resource"}).String(),
			Resource: (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(),
		},
	}

	for _, c := range candidates {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, c.URL, c.Resource, client)
		if err != nil || prm == nil {
			continue
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, fmt.Errorf("protected resource metadata at %s has no authorization_servers", c.URL)
		}
		return prm, nil
	}

	// Fallback: treat the MCP server origin as the authorization server.
	root := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	return &oauthex.ProtectedResourceMetadata{
		AuthorizationServers: []string{root},
		Resource:             mcpURL,
	}, nil
}

// awaitCallback serves a single OAuth callback on ln, expecting ?state=state.
// The handler writes a small HTML confirmation page so the user knows it
// worked. Returns the captured authorization code.
func awaitCallback(ctx context.Context, ln net.Listener, expectedState string, timeout time.Duration) (string, error) {
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := q.Get("code")
		state := q.Get("state")
		oauthErr := q.Get("error")
		switch {
		case oauthErr != "":
			desc := q.Get("error_description")
			http.Error(w, "authorization failed: "+oauthErr+": "+desc, http.StatusBadRequest)
			done <- result{err: fmt.Errorf("authorization server returned error %q: %s", oauthErr, desc)}
		case state != expectedState:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			done <- result{err: errors.New("state mismatch on OAuth callback")}
		case code == "":
			http.Error(w, "missing code", http.StatusBadRequest)
			done <- result{err: errors.New("OAuth callback missing code parameter")}
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, callbackOKPage)
			done <- result{code: code}
		}
	})

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(ln) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutCtx)
	}()

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-timeoutCtx.Done():
		return "", fmt.Errorf("timed out waiting for OAuth callback: %w", timeoutCtx.Err())
	case res := <-done:
		if res.err != nil {
			return "", res.err
		}
		return res.code, nil
	}
}

const callbackOKPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>secret-agent</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 36rem; margin: 4rem auto; line-height: 1.5;">
<h1>Authorized</h1>
<p>You can close this tab and return to the terminal.</p>
</body></html>
`

// randomState returns a URL-safe random string suitable for the OAuth state
// parameter.
func randomState() (string, error) {
	var b [24]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// authMethodToStyle maps RFC 7591 token_endpoint_auth_method names to the
// matching oauth2.AuthStyle so refresh requests send client credentials the
// way the AS expects (basic header vs. form-encoded body).
func authMethodToStyle(method string) oauth2.AuthStyle {
	switch method {
	case "client_secret_post", "none":
		return oauth2.AuthStyleInParams
	case "client_secret_basic":
		return oauth2.AuthStyleInHeader
	default:
		return oauth2.AuthStyleAutoDetect
	}
}
