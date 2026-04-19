package tool

import "context"

type senderPhoneKey struct{}

// WithSenderPhone returns a context carrying the sender's E.164 phone number,
// so tools can identify the caller without the LLM having to pass it in as a
// parameter. Signal transport populates it from the envelope; CLI leaves it
// empty.
func WithSenderPhone(ctx context.Context, phone string) context.Context {
	if phone == "" {
		return ctx
	}
	return context.WithValue(ctx, senderPhoneKey{}, phone)
}

// SenderPhoneFromContext returns the sender's E.164 phone number, or "" when
// the transport did not provide one.
func SenderPhoneFromContext(ctx context.Context) string {
	v, _ := ctx.Value(senderPhoneKey{}).(string)
	return v
}
