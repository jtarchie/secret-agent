// Package chat defines the pluggable chat transport interface.
package chat

import "context"

// Handler handles a single user message and returns the bot's reply.
type Handler func(ctx context.Context, userMsg string) (string, error)

// Transport runs a chat I/O loop against a Handler.
type Transport interface {
	Run(ctx context.Context, botName string, h Handler) error
}
