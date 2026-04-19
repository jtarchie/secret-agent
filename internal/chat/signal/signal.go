package signal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// Transport is a chat.Transport backed by signal-cli in jsonRpc mode.
// It receives DMs and group messages from Signal, forwards each to a
// chat.Dispatcher along with sender metadata, collects the reply stream,
// and sends a single Signal message back per turn.
//
// Routing (trigger matching, scope filters, prior-message buffering, and
// per-bot attachment policy) lives in the dispatcher (see internal/router).
type Transport struct {
	command  string
	account  string
	stateDir string
	verbose  int
	logger   *slog.Logger

	// outbound tracks message bodies we recently sent, keyed by body. Used
	// to suppress sync.sentMessage echoes of our own Note-to-Self replies.
	outbound sync.Map // string → time.Time
}

// outboundEchoTTL bounds how long we'll treat a sync.sentMessage as an
// echo of one of our own recent sends.
const outboundEchoTTL = 2 * time.Minute

type Option func(*Transport)

// WithCommand overrides the signal-cli binary path.
func WithCommand(c string) Option { return func(t *Transport) { t.command = c } }

// WithLogger sets a slog.Logger for signal-cli stderr and internal events.
func WithLogger(l *slog.Logger) Option { return func(t *Transport) { t.logger = l } }

// WithVerbose passes `-v` (verbose=1) or `-vv` (verbose=2) to signal-cli,
// raising its log level from WARN (the default) to INFO or DEBUG.
func WithVerbose(n int) Option { return func(t *Transport) { t.verbose = n } }

// New creates a Signal transport. account is the linked-device phone number
// (E.164); stateDir is the signal-cli state directory.
func New(account, stateDir string, opts ...Option) *Transport {
	t := &Transport{
		command:  "signal-cli",
		account:  account,
		stateDir: stateDir,
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.logger == nil {
		t.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return t
}

// Run spawns signal-cli and pumps incoming messages through the dispatcher.
// Returns when the context is canceled or signal-cli exits.
func (t *Transport) Run(ctx context.Context, botName string, dispatcher chat.Dispatcher) error {
	if t.account == "" {
		return fmt.Errorf("signal transport: account is required")
	}
	if t.stateDir == "" {
		return fmt.Errorf("signal transport: stateDir is required")
	}

	log := t.logger.With("component", "signal", "account", t.account)

	extra := verboseArgs(t.verbose)
	log.Info("spawning signal-cli",
		"command", t.command,
		"state_dir", t.stateDir,
		"verbose", t.verbose,
		"extra_args", extra,
	)

	proc, err := spawn(ctx, t.command, t.stateDir, t.account, extra...)
	if err != nil {
		return err
	}
	defer proc.close()

	go proc.forwardStderr(log)

	cli := newClient(proc.stdin)
	notifs := make(chan frame, 64)
	readDone := make(chan error, 1)
	go func() { readDone <- cli.read(proc.stdout, notifs) }()

	// Per-peer send serialization so multi-chunk replies from one peer stay
	// ordered relative to its turns. Different peers are independent.
	var peerMuM sync.Mutex
	peerMu := map[string]*sync.Mutex{}
	lockFor := func(peerID string) *sync.Mutex {
		peerMuM.Lock()
		defer peerMuM.Unlock()
		mu, ok := peerMu[peerID]
		if !ok {
			mu = &sync.Mutex{}
			peerMu[peerID] = mu
		}
		return mu
	}

	log.Info("listening for incoming messages")

	for {
		select {
		case <-ctx.Done():
			log.Info("shutdown", "reason", "context canceled", "err", ctx.Err())
			return ctx.Err()
		case err := <-readDone:
			if err != nil {
				log.Error("signal-cli reader failed", "err", err)
				return fmt.Errorf("signal-cli reader: %w", err)
			}
			log.Warn("signal-cli stdout closed")
			return fmt.Errorf("signal-cli exited")
		case f, ok := <-notifs:
			if !ok {
				log.Warn("notification channel closed")
				return fmt.Errorf("signal-cli closed")
			}
			if f.Method != "receive" {
				log.Debug("ignoring non-receive notification", "method", f.Method)
				continue
			}
			t.dispatchReceive(ctx, log, cli, dispatcher, lockFor, f)
		}
	}
}

// conversation describes where an incoming message belongs and how the
// bot should reply to it. Exactly one of recipient / groupID is set.
type conversation struct {
	key       string // stable key for session + buffer lookup
	kind      string // "dm" | "group" | "self" (for logs)
	recipient string // used for DM and Note-to-Self sends
	groupID   string // used for group sends
}

func (t *Transport) dispatchReceive(
	ctx context.Context,
	log *slog.Logger,
	cli *client,
	dispatcher chat.Dispatcher,
	lockFor func(string) *sync.Mutex,
	f frame,
) {
	var rp receiveParams
	if err := json.Unmarshal(f.Params, &rp); err != nil {
		log.Warn("decode receive params failed", "err", err)
		return
	}
	env := rp.Envelope

	dm := env.effectiveDataMessage()
	if dm == nil {
		log.Debug("ignoring envelope with no data",
			"source", env.peerID(),
			"sync", env.SyncMessage != nil,
		)
		return
	}

	text := strings.TrimSpace(dm.Message)
	atts := t.attachmentsFor(dm.Attachments)
	if text == "" && len(atts) == 0 {
		log.Debug("ignoring empty message", "source", env.peerID())
		return
	}

	conv, ok := t.classify(env, dm, log, text)
	if !ok {
		return
	}

	log.Info("received message",
		"conv", conv.key,
		"kind", conv.kind,
		"source_name", env.SourceName,
		"bytes", len(text),
		"attachments", len(atts),
	)

	chatEnv := chat.Envelope{
		ConvID:      conv.key,
		Kind:        conv.kind,
		SenderPhone: env.SourceNumber,
		GroupID:     conv.groupID,
	}
	msg := chat.Message{Text: text, Attachments: atts}

	go t.handleDM(ctx, log, cli, dispatcher, chatEnv, lockFor(conv.key), conv, msg)
}

// classify inspects an envelope and decides how to route it. Returns
// ok=false when the envelope should be dropped (outbound echo, a sync
// message destined for someone else, or a missing source).
func (t *Transport) classify(env envelope, dm *dataMessage, log *slog.Logger, text string) (conversation, bool) {
	// Group: the clearest signal, both for regular and sync data messages.
	if dm.GroupInfo != nil && dm.GroupInfo.GroupID != "" {
		return conversation{
			key:     "group:" + dm.GroupInfo.GroupID,
			kind:    "group",
			groupID: dm.GroupInfo.GroupID,
		}, true
	}

	// Sync-from-self: either a Note-to-Self we should accept, or an echo
	// of our own reply, or a DM the primary device sent to someone else
	// (which we must not treat as bot input).
	if env.isSyncFromSelf() {
		if !dm.destinationMatches(t.account) {
			log.Debug("ignoring sync sentMessage to external destination",
				"destination", dm.Destination,
			)
			return conversation{}, false
		}
		if t.isOwnEcho(text) {
			log.Debug("ignoring sync sentMessage echo of our own reply")
			return conversation{}, false
		}
		return conversation{
			key:       "self:" + t.account,
			kind:      "self",
			recipient: t.account,
		}, true
	}

	// Plain DM.
	peerID := env.peerID()
	recipient := env.peerRecipient()
	if peerID == "" || recipient == "" {
		log.Warn("dropping message with no source")
		return conversation{}, false
	}
	return conversation{
		key:       peerID,
		kind:      "dm",
		recipient: recipient,
	}, true
}

// isOwnEcho reports whether `body` matches a recently-sent outbound message
// within outboundEchoTTL. Used to suppress Note-to-Self sync echoes.
func (t *Transport) isOwnEcho(body string) bool {
	v, ok := t.outbound.Load(body)
	if !ok {
		return false
	}
	sent, _ := v.(time.Time)
	if time.Since(sent) > outboundEchoTTL {
		t.outbound.Delete(body)
		return false
	}
	// One-shot: the echo usually only arrives once. Delete so a future
	// identical user message still triggers the bot.
	t.outbound.Delete(body)
	return true
}

// rememberOutbound records a reply we just sent so an incoming sync echo
// of it can be suppressed. Periodic cleanup trims expired entries.
func (t *Transport) rememberOutbound(body string) {
	now := time.Now()
	t.outbound.Store(body, now)
	// Opportunistic GC — avoids an unbounded map in long-running processes
	// where replies keep expiring without incoming sync echoes.
	t.outbound.Range(func(k, v any) bool {
		if ts, ok := v.(time.Time); ok && now.Sub(ts) > outboundEchoTTL {
			t.outbound.Delete(k)
		}
		return true
	})
}

// attachmentsFor resolves signal-cli's attachment metadata to local file paths
// under <stateDir>/attachments/<id>, where signal-cli stores downloaded blobs.
// Per-bot attachment policy (strip or keep) is enforced by the dispatcher.
func (t *Transport) attachmentsFor(in []signalAttach) []chat.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]chat.Attachment, 0, len(in))
	for _, a := range in {
		if a.ID == "" {
			continue
		}
		out = append(out, chat.Attachment{
			Path:        filepath.Join(t.stateDir, "attachments", a.ID),
			Filename:    a.Filename,
			ContentType: a.ContentType,
		})
	}
	return out
}

func (t *Transport) handleDM(
	ctx context.Context,
	log *slog.Logger,
	cli *client,
	dispatcher chat.Dispatcher,
	chatEnv chat.Envelope,
	peerLock *sync.Mutex,
	conv conversation,
	userMsg chat.Message,
) {
	peerLog := log.With("conv", conv.key, "kind", conv.kind)
	start := time.Now()
	peerLog.Debug("handler: start", "bytes_in", len(userMsg.Text), "attachments", len(userMsg.Attachments))

	var reply strings.Builder
	var replyErr error
	chunkCount := 0

	for chunk := range dispatcher.Dispatch(ctx, chatEnv, userMsg) {
		if chunk.Err != nil {
			replyErr = chunk.Err
			continue
		}
		chunkCount++
		reply.WriteString(chunk.Delta)
	}

	body := strings.TrimSpace(reply.String())
	dur := time.Since(start)

	if replyErr != nil {
		peerLog.Error("handler: failed", "err", replyErr, "duration", dur)
		body = fmt.Sprintf("error: %s", replyErr.Error())
	} else if body == "" {
		peerLog.Debug("handler: empty reply — nothing to send", "duration", dur)
		return
	} else {
		peerLog.Info("handler: done",
			"bytes_out", len(body),
			"chunks", chunkCount,
			"duration", dur,
		)
	}

	peerLock.Lock()
	defer peerLock.Unlock()

	params := sendParams{Message: body}
	switch {
	case conv.groupID != "":
		params.GroupID = conv.groupID
	case conv.recipient != "":
		params.Recipient = []string{conv.recipient}
	default:
		peerLog.Error("handler: no reply target — nothing to send")
		return
	}

	if conv.kind == "self" {
		t.rememberOutbound(body)
	}

	sendStart := time.Now()
	if _, err := cli.call("send", params); err != nil {
		peerLog.Error("send failed", "err", err, "duration", time.Since(sendStart))
		return
	}
	peerLog.Info("send ok", "bytes", len(body), "duration", time.Since(sendStart))
}

// verboseArgs maps an integer verbosity to signal-cli's repeat-`-v` flag.
// 0 → no flag, 1 → -v (INFO), 2 → -vv (DEBUG), 3+ → -vvv (TRACE).
func verboseArgs(n int) []string {
	if n <= 0 {
		return nil
	}
	if n > 3 {
		n = 3
	}
	return []string{"-" + strings.Repeat("v", n)}
}
