// Package chat defines the pluggable chat transport interface.
package chat

import (
	"context"
	"errors"
)

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
// fields; the router picks the right scope maps based on Transport and
// matches on SenderPhone / SenderID / GroupID accordingly.
type Envelope struct {
	ConvID string // stable conversation key: peer ACI for DM, "group:<id>" for group, "self:<num>" for self
	Kind   string // "dm" | "group" | "self" | "cli"
	// Transport names the source transport: "signal" | "slack" | "cli".
	// Empty is treated as "signal" for backwards compatibility with existing
	// tests and single-transport deployments.
	Transport string
	// SenderID is the transport-native sender identifier: E.164 for Signal,
	// user ID like "U12345" for Slack, empty for CLI. Used by the router to
	// match transport-specific scope lists.
	SenderID string
	// SenderPhone is the E.164 phone when the transport has one (Signal).
	// Empty for Slack/CLI. Preserved as a dedicated field because some bot
	// tools read it via tool.WithSenderPhone.
	SenderPhone string
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
	Run(ctx context.Context, d Dispatcher) error
}

// ErrSendUnsupported is returned by Sender.Send on transports that cannot
// push unsolicited messages (e.g. the CLI TUI).
var ErrSendUnsupported = errors.New("transport does not support outbound send")

// Sender pushes an unsolicited message to a recipient without a prior
// incoming message. It is satisfied by every real transport; the CLI
// transport returns ErrSendUnsupported. The `to` format is transport-
// specific: E.164 phone or base64 group-id for Signal; U.../C.../D... IDs
// for Slack; E.164/email or chat GUID for iMessage.
type Sender interface {
	Send(ctx context.Context, to string, text string) error
}

// SenderRegistry maps a transport's configured `type:` string ("signal",
// "slack", "imessage", "cli") to its Sender. Used by the cron scheduler
// and by the built-in send_message tool/shell builtin so a bot can reply
// to a specific recipient via any configured transport.
type SenderRegistry map[string]Sender
