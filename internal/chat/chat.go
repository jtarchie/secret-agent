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

// Envelope carries the sender metadata a Dispatcher needs to route a message
// across multiple bots. Transports build it from their own native identity
// fields; the router matches on SenderPhone / GroupID.
type Envelope struct {
	ConvID      string // stable conversation key: peer ACI for DM, "group:<id>" for group, "self:<num>" for self
	Kind        string // "dm" | "group" | "self" | "cli"
	SenderPhone string // E.164 when available, else empty
	GroupID     string // populated only for group messages
}

// Dispatcher receives a preclassified message with sender metadata and
// returns a reply stream. If no route matches, implementations return a
// channel that is closed immediately with no chunks.
type Dispatcher interface {
	Dispatch(ctx context.Context, env Envelope, msg Message) <-chan Chunk
}

// Transport runs a chat I/O loop. It feeds incoming messages into the
// dispatcher and sends reply chunks back to the underlying transport.
type Transport interface {
	Run(ctx context.Context, botName string, d Dispatcher) error
}
