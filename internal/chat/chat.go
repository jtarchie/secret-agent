// Package chat defines the pluggable chat transport interface.
package chat

import "context"

// Chunk is one piece of a streaming bot reply. A non-nil Err is terminal;
// the channel is closed immediately after.
type Chunk struct {
	Delta string
	Err   error
}

// Handler handles a single user message and returns a channel of reply chunks.
// The channel is closed when the turn completes.
type Handler func(ctx context.Context, userMsg string) <-chan Chunk

// Transport runs a chat I/O loop against a Handler.
type Transport interface {
	Run(ctx context.Context, botName string, h Handler) error
}
