// Package signal implements a chat transport backed by signal-cli in jsonRpc mode.
package signal

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// frame is the tagged union of JSON-RPC 2.0 messages received from signal-cli.
// A frame with a non-empty Method (and any ID or no ID) is a notification
// (e.g. "receive"). A frame with a non-nil Result or Error field is a
// response to a request we sent.
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("signal-cli rpc error %d: %s", e.Code, e.Message)
}

// outRequest is the shape we encode when sending requests to signal-cli.
type outRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// client multiplexes JSON-RPC calls and notifications over newline-delimited
// stdio. It owns neither the reader nor the writer; callers close them.
type client struct {
	enc    *json.Encoder
	writeM sync.Mutex

	nextID atomic.Int64

	pendingM sync.Mutex
	pending  map[int64]chan frame
}

func newClient(w io.Writer) *client {
	return &client{
		enc:     json.NewEncoder(w),
		pending: make(map[int64]chan frame),
	}
}

// call sends a request and blocks until the matching response (or an error).
// notifs receives any notification frames (Method != "") observed while the
// reader loop runs; it is closed when reader returns.
func (c *client) call(method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan frame, 1)

	c.pendingM.Lock()
	c.pending[id] = ch
	c.pendingM.Unlock()

	defer func() {
		c.pendingM.Lock()
		delete(c.pending, id)
		c.pendingM.Unlock()
	}()

	c.writeM.Lock()
	err := c.enc.Encode(outRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	c.writeM.Unlock()
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", method, err)
	}

	resp, ok := <-ch
	if !ok {
		return nil, fmt.Errorf("signal-cli closed before responding to %s", method)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// read pumps frames from r. Response frames are routed to pending callers;
// notification frames are pushed onto notifs. read returns when r closes
// or a decode error occurs.
func (c *client) read(r io.Reader, notifs chan<- frame) error {
	defer func() {
		c.pendingM.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.pendingM.Unlock()
		close(notifs)
	}()

	// Use a large buffer — signal-cli receive payloads can include attachments
	// metadata and grow past the default 64 KiB token limit.
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var f frame
		err := json.Unmarshal(line, &f)
		if err != nil {
			return fmt.Errorf("decode frame: %w (raw: %s)", err, string(line))
		}
		c.dispatch(f, notifs)
	}
	err := scanner.Err()
	if err != nil {
		return fmt.Errorf("scan signal-cli output: %w", err)
	}
	return nil
}

func (c *client) dispatch(f frame, notifs chan<- frame) {
	// Frames with a method are notifications (or server-to-client requests).
	// signal-cli only emits notifications.
	if f.Method != "" {
		notifs <- f
		return
	}
	// Response — route by id.
	if len(f.ID) == 0 {
		return
	}
	var id int64
	err := json.Unmarshal(f.ID, &id)
	if err != nil {
		return
	}
	c.pendingM.Lock()
	ch, ok := c.pending[id]
	c.pendingM.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- f:
	default:
	}
}

// --- signal-cli payload shapes we care about -------------------------------

// envelope mirrors the envelope object in signal-cli's receive notification.
// We only decode fields the transport uses.
type envelope struct {
	Source       string       `json:"source"`
	SourceNumber string       `json:"sourceNumber"`
	SourceUuid   string       `json:"sourceUuid"`
	SourceName   string       `json:"sourceName"`
	Timestamp    int64        `json:"timestamp"`
	DataMessage  *dataMessage `json:"dataMessage,omitempty"`
	SyncMessage  *syncMessage `json:"syncMessage,omitempty"`
}

type dataMessage struct {
	Timestamp   int64          `json:"timestamp"`
	Message     string         `json:"message"`
	GroupInfo   *groupInfo     `json:"groupInfo,omitempty"`
	Attachments []signalAttach `json:"attachments,omitempty"`

	// Destination* are populated on syncMessage.sentMessage — they describe
	// who the primary device sent the message to. For Note-to-Self the
	// destination matches the account itself.
	Destination       string `json:"destination,omitempty"`
	DestinationNumber string `json:"destinationNumber,omitempty"`
	DestinationUuid   string `json:"destinationUuid,omitempty"`
}

type signalAttach struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
}

type groupInfo struct {
	GroupID string `json:"groupId"`
	Type    string `json:"type"`
}

// syncMessage is set for messages originating from other devices on the
// same account — e.g. the primary phone sending a DM to a contact, or
// sending a message to "Note to Self". Most of these are echoes we skip;
// the Note-to-Self case is the one we actually want to handle so that
// users can test the bot by messaging themselves.
type syncMessage struct {
	SentMessage *dataMessage `json:"sentMessage,omitempty"`
}

type receiveParams struct {
	Account  string   `json:"account,omitempty"`
	Envelope envelope `json:"envelope"`
}

// sendParams is the request payload for the "send" method.
type sendParams struct {
	Account   string   `json:"account,omitempty"`
	Recipient []string `json:"recipient,omitempty"`
	GroupID   string   `json:"groupId,omitempty"`
	Message   string   `json:"message"`
}

// peerID chooses the stable identifier we use to key ADK sessions. UUID is
// preferred (stable across number changes); fall back to phone number.
func (e envelope) peerID() string {
	if e.SourceUuid != "" {
		return e.SourceUuid
	}
	if e.SourceNumber != "" {
		return e.SourceNumber
	}
	return e.Source
}

// peerRecipient is what we pass back to signal-cli as the recipient. The
// phone number form is what "send" expects; fall back to UUID if needed.
func (e envelope) peerRecipient() string {
	if e.SourceNumber != "" {
		return e.SourceNumber
	}
	if e.Source != "" {
		return e.Source
	}
	return e.SourceUuid
}

// effectiveDataMessage returns the DataMessage we should treat as the user's
// input, preferring a direct DataMessage and falling back to a
// SyncMessage.SentMessage (used for Note-to-Self). Returns nil when the
// envelope carries neither.
func (e envelope) effectiveDataMessage() *dataMessage {
	if e.DataMessage != nil {
		return e.DataMessage
	}
	if e.SyncMessage != nil {
		return e.SyncMessage.SentMessage
	}
	return nil
}

// isSyncFromSelf reports whether this envelope is a sync echo — i.e. a
// message that originated on another device of this same account. The
// caller uses this to decide whether to treat the sentMessage as Note-to-
// Self (destination == account) or to drop it (destination == someone else,
// or destination == account but it is our own outbound echo).
func (e envelope) isSyncFromSelf() bool {
	return e.DataMessage == nil && e.SyncMessage != nil && e.SyncMessage.SentMessage != nil
}

// destinationMatches reports whether the SentMessage's destination equals
// the given account (phone or UUID). Used to detect Note-to-Self.
func (d *dataMessage) destinationMatches(account string) bool {
	if d == nil || account == "" {
		return false
	}
	return d.Destination == account ||
		d.DestinationNumber == account ||
		d.DestinationUuid == account
}
