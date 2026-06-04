package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/mcpauth"
)

// McpCmd is the grouping for OAuth/login operations against HTTP MCP servers
// declared in a bot YAML. Nested so we can grow it later (logout, list,
// refresh) without polluting the top-level CLI.
type McpCmd struct {
	Login McpLoginCmd `cmd:"" help:"authorize an HTTP MCP server and cache the resulting token"`
}

// McpLoginCmd walks the OAuth 2.1 + PKCE flow against one or every HTTP MCP
// server declared in a bot YAML, opening the user's browser for consent and
// persisting the resulting credentials per-bot.
type McpLoginCmd struct {
	Bot string `arg:"" help:"path to the agent YAML" type:"existingfile"`

	MCP       string        `help:"limit to a single MCP server by name (default: every server with url:)" name:"mcp"`
	Listen    string        `default:"127.0.0.1:0"                                                         help:"local TCP bind for the OAuth callback"     name:"listen"`
	NoBrowser bool          `help:"do not open a browser; print the URL for manual paste instead"          name:"no-browser"`
	Timeout   time.Duration `default:"5m"                                                                  help:"per-server wait for browser consent"       name:"timeout"`
	Scopes    []string      `help:"override discovered scopes (comma-separated or repeatable)"             name:"scopes"`
}

func (c *McpLoginCmd) Run() error {
	b, err := bot.Load(c.Bot)
	if err != nil {
		return fmt.Errorf("load bot: %w", err)
	}

	servers := selectHTTPServers(b, c.MCP)
	if len(servers) == 0 {
		if c.MCP != "" {
			return fmt.Errorf("no HTTP MCP server named %q in %s", c.MCP, c.Bot)
		}
		return fmt.Errorf("bot %q has no HTTP MCP servers to authorize", b.Name)
	}

	store, err := mcpauth.Open()
	if err != nil {
		return fmt.Errorf("open mcp auth store: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	scopes := flattenScopes(c.Scopes)
	var loginErr error
	for _, m := range servers {
		_, err = mcpauth.Login(ctx, store, mcpauth.LoginConfig{
			BotName:   b.Name,
			MCPName:   m.Name,
			MCPURL:    m.URL,
			Listen:    c.Listen,
			NoBrowser: c.NoBrowser,
			Out:       os.Stderr,
			Scopes:    scopes,
			Timeout:   c.Timeout,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "x %s: %v\n", m.Name, err)
			loginErr = errors.Join(loginErr, fmt.Errorf("%s: %w", m.Name, err))
			continue
		}
		fmt.Fprintf(os.Stderr, "ok %s\n", m.Name)
	}
	return loginErr
}

// selectHTTPServers filters the bot's MCP list to servers configured with a
// URL transport. When mcpName is non-empty only that server is returned.
func selectHTTPServers(b *bot.Bot, mcpName string) []bot.MCPServer {
	var out []bot.MCPServer
	for _, m := range b.MCP {
		if m.URL == "" {
			continue
		}
		if mcpName != "" && m.Name != mcpName {
			continue
		}
		out = append(out, m)
	}
	return out
}

// flattenScopes turns ["read,write", "delete"] into ["read","write","delete"]
// so users can pass --scopes read,write or --scopes read --scopes write.
func flattenScopes(raw []string) []string {
	var out []string
	for _, r := range raw {
		for _, s := range strings.Split(r, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
