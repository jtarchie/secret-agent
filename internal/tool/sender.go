package tool

import (
	"context"

	"github.com/jtarchie/secret-agent/internal/chat"
)

type senderInfoKey struct{}

// senderInfo is the transport-agnostic identity carried through tool context.
// Individual fields may be empty when the transport did not supply them
// (e.g. SenderPhone is empty on Slack; all fields are empty on the CLI
// transport since there is no external sender).
type senderInfo struct {
	SenderID        string
	SenderPhone     string
	SenderTransport string
	ConvID          string
}

func identityFromContext(ctx context.Context) senderInfo {
	v, _ := ctx.Value(senderInfoKey{}).(senderInfo)
	return v
}

// WithEnvelope returns a context carrying the sender's identity and the
// conversation ID extracted from a chat.Envelope. Tools read the individual
// fields via SenderIDFromContext, SenderPhoneFromContext, etc.
func WithEnvelope(ctx context.Context, env chat.Envelope) context.Context {
	return context.WithValue(ctx, senderInfoKey{}, senderInfo{
		SenderID:        env.SenderID,
		SenderPhone:     env.SenderPhone,
		SenderTransport: env.Transport,
		ConvID:          env.ConvID,
	})
}

// WithSenderPhone returns a context carrying the sender's E.164 phone number.
// Preserved for callers and tests that only care about the phone; under the
// hood it merges into the same identity value WithEnvelope populates.
func WithSenderPhone(ctx context.Context, phone string) context.Context {
	if phone == "" {
		return ctx
	}
	info := identityFromContext(ctx)
	info.SenderPhone = phone
	return context.WithValue(ctx, senderInfoKey{}, info)
}

// SenderPhoneFromContext returns the sender's E.164 phone number, or "" when
// the transport did not provide one (Slack, CLI).
func SenderPhoneFromContext(ctx context.Context) string {
	return identityFromContext(ctx).SenderPhone
}

// SenderIDFromContext returns a transport-native sender identifier: E.164
// on Signal, a Slack user ID on Slack, "" on CLI.
func SenderIDFromContext(ctx context.Context) string {
	return identityFromContext(ctx).SenderID
}

// SenderTransportFromContext returns "signal", "slack", "cli", or "" when
// unset.
func SenderTransportFromContext(ctx context.Context) string {
	return identityFromContext(ctx).SenderTransport
}

// ConvIDFromContext returns the stable conversation key (DM, thread, or
// group) the current turn belongs to, or "" when unset.
func ConvIDFromContext(ctx context.Context) string {
	return identityFromContext(ctx).ConvID
}
