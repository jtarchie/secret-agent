package tool

import (
	"testing"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
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

func TestEnvelopeRoundtrip(t *testing.T) {
	env := chat.Envelope{
		SenderID:    "U123",
		SenderPhone: "+15551234567",
		Transport:   "slack",
		ConvID:      "D123:1700000000.000100",
	}
	ctx := WithEnvelope(t.Context(), env)

	if got := SenderIDFromContext(ctx); got != "U123" {
		t.Errorf("SenderID: got %q, want U123", got)
	}
	if got := SenderPhoneFromContext(ctx); got != "+15551234567" {
		t.Errorf("SenderPhone: got %q, want +15551234567", got)
	}
	if got := SenderTransportFromContext(ctx); got != "slack" {
		t.Errorf("SenderTransport: got %q, want slack", got)
	}
	if got := ConvIDFromContext(ctx); got != "D123:1700000000.000100" {
		t.Errorf("ConvID: got %q, want D123:1700000000.000100", got)
	}
}

func TestEnvelopeUnsetReturnsEmpty(t *testing.T) {
	ctx := t.Context()
	if got := SenderIDFromContext(ctx); got != "" {
		t.Errorf("SenderID: got %q, want empty", got)
	}
	if got := SenderTransportFromContext(ctx); got != "" {
		t.Errorf("SenderTransport: got %q, want empty", got)
	}
	if got := ConvIDFromContext(ctx); got != "" {
		t.Errorf("ConvID: got %q, want empty", got)
	}
}

func TestBuildRuntimeEnvSeedsSenderPhone(t *testing.T) {
	env, err := buildRuntimeEnv("t", nil, nil, nil, senderInfo{SenderPhone: "+15551234567"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if env["sender_phone"] != "+15551234567" {
		t.Fatalf("sender_phone: got %v, want +15551234567", env["sender_phone"])
	}
}

func TestBuildRuntimeEnvSeedsAllIdentity(t *testing.T) {
	id := senderInfo{
		SenderID:        "U123",
		SenderPhone:     "+15551234567",
		SenderTransport: "slack",
		ConvID:          "D123:1.000",
	}
	env, err := buildRuntimeEnv("t", nil, nil, nil, id)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if env["sender_id"] != "U123" {
		t.Errorf("sender_id: got %v, want U123", env["sender_id"])
	}
	if env["sender_transport"] != "slack" {
		t.Errorf("sender_transport: got %v, want slack", env["sender_transport"])
	}
	if env["conv_id"] != "D123:1.000" {
		t.Errorf("conv_id: got %v, want D123:1.000", env["conv_id"])
	}
	if env["sender_phone"] != "+15551234567" {
		t.Errorf("sender_phone: got %v, want +15551234567", env["sender_phone"])
	}
}

func TestBuildRuntimeEnvUserParamOverridesSenderPhone(t *testing.T) {
	params := map[string]bot.Param{
		"sender_phone": {Type: bot.ParamString, Required: true},
	}
	args := map[string]any{"sender_phone": "user-supplied"}
	env, err := buildRuntimeEnv("t", params, args, nil, senderInfo{SenderPhone: "+15551234567"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if env["sender_phone"] != "user-supplied" {
		t.Fatalf("user param should win, got %v", env["sender_phone"])
	}
}

func TestBuildRuntimeEnvUserParamOverridesSenderID(t *testing.T) {
	params := map[string]bot.Param{
		"sender_id": {Type: bot.ParamString, Required: true},
	}
	args := map[string]any{"sender_id": "user-supplied"}
	env, err := buildRuntimeEnv("t", params, args, nil, senderInfo{SenderID: "U123"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if env["sender_id"] != "user-supplied" {
		t.Fatalf("user param should win, got %v", env["sender_id"])
	}
}
