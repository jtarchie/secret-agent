package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yml")
	err := os.WriteFile(p, []byte(body), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestLoadSignalOnly(t *testing.T) {
	p := write(t, `
bots:
  - bot.yml
transports:
  - type: signal
    account: "+15551234567"
    state_dir: /tmp/signal
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Transports) != 1 || cfg.Transports[0].Type != TransportSignal {
		t.Fatalf("unexpected transports: %+v", cfg.Transports)
	}
	if cfg.Transports[0].Account != "+15551234567" || cfg.Transports[0].StateDir != "/tmp/signal" {
		t.Errorf("signal fields not parsed: %+v", cfg.Transports[0])
	}
}

func TestLoadSlackOnlyResolvesEnvTokens(t *testing.T) {
	t.Setenv("TEST_BOT", "xoxb-secret")
	t.Setenv("TEST_APP", "xapp-secret")
	p := write(t, `
bots: [bot.yml]
transports:
  - type: slack
    bot_token_env: TEST_BOT
    app_token_env: TEST_APP
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tr := cfg.Transports[0]
	if tr.BotToken != "xoxb-secret" || tr.AppToken != "xapp-secret" {
		t.Errorf("tokens not resolved: %+v", tr)
	}
}

func TestLoadSlackRejectsEmptyEnvVar(t *testing.T) {
	t.Setenv("NEVER_SET_BOT", "")
	t.Setenv("NEVER_SET_APP", "")
	p := write(t, `
bots: [b.yml]
transports:
  - type: slack
    bot_token_env: NEVER_SET_BOT
    app_token_env: NEVER_SET_APP
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected empty-env error")
	}
	if !strings.Contains(err.Error(), "NEVER_SET_BOT") {
		t.Errorf("error should name the missing env var: %v", err)
	}
}

func TestLoadBothTransports(t *testing.T) {
	t.Setenv("B", "xoxb-1")
	t.Setenv("A", "xapp-1")
	p := write(t, `
bots: [b.yml]
transports:
  - type: signal
    account: "+15551234567"
    state_dir: /tmp/s
  - type: slack
    bot_token_env: B
    app_token_env: A
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Transports) != 2 {
		t.Fatalf("want 2 transports, got %d", len(cfg.Transports))
	}
}

func TestLoadRejectsCLIWithOthers(t *testing.T) {
	p := write(t, `
bots: [b.yml]
transports:
  - type: cli
  - type: signal
    account: "+15551234567"
    state_dir: /tmp/s
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected rejection of cli + signal")
	}
	if !strings.Contains(err.Error(), "cli") {
		t.Errorf("error should mention cli: %v", err)
	}
}

func TestLoadRejectsDuplicateTransports(t *testing.T) {
	p := write(t, `
bots: [b.yml]
transports:
  - type: signal
    account: "+15551234567"
    state_dir: /tmp/s
  - type: signal
    account: "+15557654321"
    state_dir: /tmp/s2
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected duplicate-transport error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate: %v", err)
	}
}

func TestLoadRejectsEmptyBots(t *testing.T) {
	p := write(t, `
transports:
  - type: cli
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected empty-bots error")
	}
	if !strings.Contains(err.Error(), "bots") {
		t.Errorf("error should mention bots: %v", err)
	}
}

func TestLoadRejectsEmptyTransports(t *testing.T) {
	p := write(t, `
bots: [b.yml]
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected empty-transports error")
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Errorf("error should mention transport: %v", err)
	}
}

func TestLoadRejectsUnknownType(t *testing.T) {
	p := write(t, `
bots: [b.yml]
transports:
  - type: carrier-pigeon
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected unknown-type error")
	}
	if !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("error should mention unknown type: %v", err)
	}
}

func TestLoadSignalResolvesAccountEnv(t *testing.T) {
	t.Setenv("TEST_SIGNAL_ACCOUNT", "+15551234567")
	p := write(t, `
bots: [b.yml]
transports:
  - type: signal
    account_env: TEST_SIGNAL_ACCOUNT
    state_dir: /tmp/s
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Transports[0].Account != "+15551234567" {
		t.Errorf("account_env not resolved: %+v", cfg.Transports[0])
	}
}

func TestLoadSignalRejectsBothAccountForms(t *testing.T) {
	t.Setenv("TEST_SIGNAL_ACCOUNT", "+15551234567")
	p := write(t, `
bots: [b.yml]
transports:
  - type: signal
    account: "+15557654321"
    account_env: TEST_SIGNAL_ACCOUNT
    state_dir: /tmp/s
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected rejection when both account and account_env set")
	}
	if !strings.Contains(err.Error(), "only one of account or account_env") {
		t.Errorf("error should name the conflict: %v", err)
	}
}

func TestLoadSignalRejectsEmptyAccountEnv(t *testing.T) {
	t.Setenv("NEVER_SET_ACCOUNT", "")
	p := write(t, `
bots: [b.yml]
transports:
  - type: signal
    account_env: NEVER_SET_ACCOUNT
    state_dir: /tmp/s
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected empty-env error")
	}
	if !strings.Contains(err.Error(), "NEVER_SET_ACCOUNT") {
		t.Errorf("error should name the missing env var: %v", err)
	}
}

func TestLoadSignalRejectsNeitherAccountForm(t *testing.T) {
	p := write(t, `
bots: [b.yml]
transports:
  - type: signal
    state_dir: /tmp/s
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected missing-account error")
	}
	if !strings.Contains(err.Error(), "account") {
		t.Errorf("error should mention account: %v", err)
	}
}

func TestLoadRejectsSignalMissingFields(t *testing.T) {
	p := write(t, `
bots: [b.yml]
transports:
  - type: signal
    account: "+15551234567"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected missing-state_dir error")
	}
	if !strings.Contains(err.Error(), "state_dir") {
		t.Errorf("error should mention state_dir: %v", err)
	}
}
