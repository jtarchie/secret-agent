// Package config parses the secret-agent run configuration: the list of
// bot YAML paths to serve and the list of chat transports (Signal, Slack,
// iMessage, or CLI) to pump messages through. A single Config instance is
// the source of truth passed to cmd/secret-agent.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level shape of the run config file.
type Config struct {
	// Bots is an ordered list of bot YAML paths. Paths are relative to the
	// config file's directory when not absolute.
	Bots []string `yaml:"bots"`
	// Transports lists the chat transports to fan out in parallel.
	Transports []Transport `yaml:"transports"`
}

// TransportType identifies one of the supported transport implementations.
type TransportType string

const (
	TransportSignal   TransportType = "signal"
	TransportSlack    TransportType = "slack"
	TransportIMessage TransportType = "imessage"
	TransportCLI      TransportType = "cli"
)

// Transport is a single transport stanza in the config file.
type Transport struct {
	Type TransportType `yaml:"type"`

	// Signal fields. Exactly one of Account or AccountEnv must be set; the
	// env form mirrors Slack's bot_token_env pattern so the E.164 number can
	// live outside the repo.
	Account    string `yaml:"account,omitempty"`
	AccountEnv string `yaml:"account_env,omitempty"`
	StateDir   string `yaml:"state_dir,omitempty"`
	Command    string `yaml:"command,omitempty"`
	Verbose    int    `yaml:"verbose,omitempty"`

	// Slack fields. Secrets must be supplied via env var indirection.
	BotTokenEnv string `yaml:"bot_token_env,omitempty"`
	AppTokenEnv string `yaml:"app_token_env,omitempty"`
	// Resolved secrets, populated by Load. Not read from YAML.
	BotToken string `yaml:"-"`
	AppToken string `yaml:"-"`

	// iMessage fields (backed by a BlueBubbles Server). The server password
	// must be supplied via env var indirection like Slack's tokens.
	ServerURL      string `yaml:"server_url,omitempty"`
	PasswordEnv    string `yaml:"password_env,omitempty"`
	Password       string `yaml:"-"`
	// WebhookListen is the host:port our HTTP listener binds to; BlueBubbles
	// Server must be configured to POST to http://<this addr><webhook_path>.
	// Optional; defaults are chosen by the transport.
	WebhookListen  string `yaml:"webhook_listen,omitempty"`
	WebhookPath    string `yaml:"webhook_path,omitempty"`

	// MessagePrefix is prepended verbatim to every outgoing reply (including
	// error bodies). Mainly useful on Signal, where bot messages are otherwise
	// indistinguishable from a human's. Preserved as-is — no trimming — so a
	// trailing space or newline in the configured value is kept.
	MessagePrefix string `yaml:"message_prefix,omitempty"`
}

// Load reads the config file at path and validates it. Env-var indirection
// for Slack tokens is resolved here; a missing env var is a hard error.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if len(cfg.Bots) == 0 {
		return nil, fmt.Errorf("%s: at least one bot path is required in `bots:`", path)
	}
	for i, p := range cfg.Bots {
		cfg.Bots[i] = strings.TrimSpace(p)
		if cfg.Bots[i] == "" {
			return nil, fmt.Errorf("%s: bots[%d]: must not be empty", path, i)
		}
	}

	if len(cfg.Transports) == 0 {
		return nil, fmt.Errorf("%s: at least one transport is required in `transports:`", path)
	}

	seen := make(map[TransportType]int, len(cfg.Transports))
	for i := range cfg.Transports {
		t := &cfg.Transports[i]
		t.Type = TransportType(strings.TrimSpace(string(t.Type)))
		if t.Type == "" {
			return nil, fmt.Errorf("%s: transports[%d]: type is required", path, i)
		}
		if first, dup := seen[t.Type]; dup {
			return nil, fmt.Errorf("%s: transports[%d]: duplicate type %q (first declared at transports[%d])", path, i, t.Type, first)
		}
		seen[t.Type] = i
		err := normalizeTransport(t, i, path)
		if err != nil {
			return nil, err
		}
	}

	// CLI transport is mutually exclusive: bubbletea owns stdin/stdout.
	if _, cliSeen := seen[TransportCLI]; cliSeen && len(cfg.Transports) > 1 {
		return nil, fmt.Errorf("%s: transports: `cli` cannot be combined with other transports (it owns stdin/stdout)", path)
	}

	return &cfg, nil
}

func normalizeTransport(t *Transport, i int, path string) error {
	switch t.Type {
	case TransportSignal:
		return normalizeSignal(t, i, path)
	case TransportSlack:
		return normalizeSlack(t, i, path)
	case TransportIMessage:
		return normalizeIMessage(t, i, path)
	case TransportCLI:
		return nil
	default:
		return fmt.Errorf("%s: transports[%d]: unknown type %q (want signal|slack|imessage|cli)", path, i, t.Type)
	}
}

func normalizeSignal(t *Transport, i int, path string) error {
	t.Account = strings.TrimSpace(t.Account)
	t.AccountEnv = strings.TrimSpace(t.AccountEnv)
	t.StateDir = strings.TrimSpace(t.StateDir)
	t.Command = strings.TrimSpace(t.Command)
	switch {
	case t.Account != "" && t.AccountEnv != "":
		return fmt.Errorf("%s: transports[%d] (signal): only one of account or account_env may be set", path, i)
	case t.Account == "" && t.AccountEnv == "":
		return fmt.Errorf("%s: transports[%d] (signal): exactly one of account or account_env is required", path, i)
	case t.AccountEnv != "":
		v := os.Getenv(t.AccountEnv)
		if v == "" {
			return fmt.Errorf("%s: transports[%d] (signal): $%s is empty", path, i, t.AccountEnv)
		}
		t.Account = v
	}
	if t.StateDir == "" {
		return fmt.Errorf("%s: transports[%d] (signal): state_dir is required", path, i)
	}
	return nil
}

func normalizeIMessage(t *Transport, i int, path string) error {
	t.ServerURL = strings.TrimSpace(t.ServerURL)
	t.PasswordEnv = strings.TrimSpace(t.PasswordEnv)
	t.StateDir = strings.TrimSpace(t.StateDir)
	t.WebhookListen = strings.TrimSpace(t.WebhookListen)
	t.WebhookPath = strings.TrimSpace(t.WebhookPath)
	if t.ServerURL == "" {
		return fmt.Errorf("%s: transports[%d] (imessage): server_url is required", path, i)
	}
	if t.PasswordEnv == "" {
		return fmt.Errorf("%s: transports[%d] (imessage): password_env is required", path, i)
	}
	if t.StateDir == "" {
		return fmt.Errorf("%s: transports[%d] (imessage): state_dir is required", path, i)
	}
	pw := os.Getenv(t.PasswordEnv)
	if pw == "" {
		return fmt.Errorf("%s: transports[%d] (imessage): $%s is empty", path, i, t.PasswordEnv)
	}
	t.Password = pw
	return nil
}

func normalizeSlack(t *Transport, i int, path string) error {
	t.BotTokenEnv = strings.TrimSpace(t.BotTokenEnv)
	t.AppTokenEnv = strings.TrimSpace(t.AppTokenEnv)
	if t.BotTokenEnv == "" {
		return fmt.Errorf("%s: transports[%d] (slack): bot_token_env is required", path, i)
	}
	if t.AppTokenEnv == "" {
		return fmt.Errorf("%s: transports[%d] (slack): app_token_env is required", path, i)
	}
	bot := os.Getenv(t.BotTokenEnv)
	if bot == "" {
		return fmt.Errorf("%s: transports[%d] (slack): $%s is empty", path, i, t.BotTokenEnv)
	}
	app := os.Getenv(t.AppTokenEnv)
	if app == "" {
		return fmt.Errorf("%s: transports[%d] (slack): $%s is empty", path, i, t.AppTokenEnv)
	}
	t.BotToken = bot
	t.AppToken = app
	return nil
}
