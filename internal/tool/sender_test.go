package tool

import (
	"testing"

	"github.com/jtarchie/secret-agent/internal/bot"
)

func TestSenderPhoneRoundtrip(t *testing.T) {
	ctx := WithSenderPhone(t.Context(), "+15551234567")
	if got := SenderPhoneFromContext(ctx); got != "+15551234567" {
		t.Fatalf("got %q, want +15551234567", got)
	}
}

func TestSenderPhoneEmptyDoesNotStore(t *testing.T) {
	ctx := WithSenderPhone(t.Context(), "")
	if got := SenderPhoneFromContext(ctx); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestSenderPhoneUnsetReturnsEmpty(t *testing.T) {
	if got := SenderPhoneFromContext(t.Context()); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestBuildRuntimeEnvSeedsSenderPhone(t *testing.T) {
	env, err := buildRuntimeEnv("t", nil, nil, nil, "+15551234567")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if env["sender_phone"] != "+15551234567" {
		t.Fatalf("sender_phone: got %v, want +15551234567", env["sender_phone"])
	}
}

func TestBuildRuntimeEnvUserParamOverridesSenderPhone(t *testing.T) {
	params := map[string]bot.Param{
		"sender_phone": {Type: bot.ParamString, Required: true},
	}
	args := map[string]any{"sender_phone": "user-supplied"}
	env, err := buildRuntimeEnv("t", params, args, nil, "+15551234567")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if env["sender_phone"] != "user-supplied" {
		t.Fatalf("user param should win, got %v", env["sender_phone"])
	}
}
