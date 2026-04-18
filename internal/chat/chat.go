// Package chat defines the pluggable chat transport interface.
package chat

import "context"

// Chunk is one piece of a streaming bot reply. A non-nil Err is terminal;
// the channel is closed immediately after.
type Chunk struct {
	Delta string
	Err   error
}

// Attachment is a file the user included with a message. The runtime reads
// Path from local disk, so the transport must ensure the file is available
// there before dispatching.
type Attachment struct {
	Path        string
	Filename    string
	ContentType string
}

// Message is a single user turn: free-form text plus any attached files.
type Message struct {
	Text        string
	Attachments []Attachment
}

// Handler handles a single user message and returns a channel of reply chunks.
// The channel is closed when the turn completes.
type Handler func(ctx context.Context, msg Message) <-chan Chunk

// HandlerFactory returns a Handler bound to a specific conversation ID.
// Single-peer transports (CLI) call it once with a constant ID; multi-peer
// transports (Signal) call it per peer.
type HandlerFactory func(convID string) Handler

// Transport runs a chat I/O loop. It obtains per-conversation Handlers
// from the factory as needed.
type Transport interface {
	Run(ctx context.Context, botName string, newHandler HandlerFactory) error
}
