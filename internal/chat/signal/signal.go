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
// It receives DMs from Signal, forwards each to a per-peer chat.Handler,
// buffers the reply, and sends a single Signal message back per turn.
//
// Group messages are ignored.
type Transport struct {
	command  string
	account  string
	stateDir string
	verbose  int
	logger   *slog.Logger

	triggers []string
	matcher  *triggerMatcher
	buffers  sync.Map // peerID → *peerBuffer
}

type Option func(*Transport)

// WithCommand overrides the signal-cli binary path.
func WithCommand(c string) Option { return func(t *Transport) { t.command = c } }

// WithLogger sets a slog.Logger for signal-cli stderr and internal events.
func WithLogger(l *slog.Logger) Option { return func(t *Transport) { t.logger = l } }

// WithVerbose passes `-v` (verbose=1) or `-vv` (verbose=2) to signal-cli,
// raising its log level from WARN (the default) to INFO or DEBUG.
func WithVerbose(n int) Option { return func(t *Transport) { t.verbose = n } }

// WithTriggers configures the set of trigger words the bot waits for before
// replying. An empty or nil slice keeps the pre-trigger behavior (reply to
// every DM). Messages without any trigger are buffered per-peer and bundled
// into the next triggered turn as prior context.
func WithTriggers(words []string) Option {
	return func(t *Transport) { t.triggers = append(t.triggers[:0], words...) }
}

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

// Run spawns signal-cli and pumps incoming DMs through newHandler(peerID).
// Returns when the context is canceled or signal-cli exits.
func (t *Transport) Run(ctx context.Context, botName string, newHandler chat.HandlerFactory) error {
	if t.account == "" {
		return fmt.Errorf("signal transport: account is required")
	}
	if t.stateDir == "" {
		return fmt.Errorf("signal transport: stateDir is required")
	}

	matcher, err := newTriggerMatcher(t.triggers)
	if err != nil {
		return fmt.Errorf("signal transport: compile triggers: %w", err)
	}
	t.matcher = matcher

	log := t.logger.With("component", "signal", "account", t.account)
	if t.matcher != nil {
		log.Info("trigger-word gating enabled", "triggers", t.triggers, "buffer_cap", peerBufferCap)
	}

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
			t.dispatchReceive(ctx, log, cli, newHandler, lockFor, f)
		}
	}
}

func (t *Transport) dispatchReceive(
	ctx context.Context,
	log *slog.Logger,
	cli *client,
	newHandler chat.HandlerFactory,
	lockFor func(string) *sync.Mutex,
	f frame,
) {
	var rp receiveParams
	if err := json.Unmarshal(f.Params, &rp); err != nil {
		log.Warn("decode receive params failed", "err", err)
		return
	}
	env := rp.Envelope

	if env.DataMessage == nil {
		log.Debug("ignoring envelope with no dataMessage",
			"source", env.peerID(),
			"sync", env.SyncMessage != nil,
		)
		return
	}
	if env.DataMessage.GroupInfo != nil {
		log.Debug("ignoring group message",
			"source", env.peerID(),
			"group_id", env.DataMessage.GroupInfo.GroupID,
		)
		return
	}

	text := strings.TrimSpace(env.DataMessage.Message)
	atts := t.attachmentsFor(env.DataMessage.Attachments)
	if text == "" && len(atts) == 0 {
		log.Debug("ignoring empty message", "source", env.peerID())
		return
	}

	peerID := env.peerID()
	recipient := env.peerRecipient()
	if peerID == "" || recipient == "" {
		log.Warn("dropping message with no source")
		return
	}

	buf := t.bufferFor(peerID)
	if t.matcher != nil && !t.matcher.Matches(text) {
		buf.Append(text)
		log.Info("buffered untriggered message",
			"peer", peerID,
			"bytes", len(text),
			"dropped_attachments", len(atts),
		)
		return
	}

	if prior := buf.Drain(); len(prior) > 0 {
		log.Info("flushing buffered prior messages into turn",
			"peer", peerID,
			"prior_count", len(prior),
		)
		text = wrapWithPrior(prior, text)
	}

	log.Info("received DM",
		"peer", peerID,
		"source_name", env.SourceName,
		"bytes", len(text),
		"attachments", len(atts),
	)
	msg := chat.Message{Text: text, Attachments: atts}
	go t.handleDM(ctx, log, cli, newHandler(peerID), lockFor(peerID), peerID, recipient, msg)
}

// bufferFor returns the per-peer message buffer, creating it on first use.
func (t *Transport) bufferFor(peerID string) *peerBuffer {
	if v, ok := t.buffers.Load(peerID); ok {
		return v.(*peerBuffer)
	}
	v, _ := t.buffers.LoadOrStore(peerID, &peerBuffer{})
	return v.(*peerBuffer)
}

// attachmentsFor resolves signal-cli's attachment metadata to local file paths
// under <stateDir>/attachments/<id>, where signal-cli stores downloaded blobs.
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
	handler chat.Handler,
	peerLock *sync.Mutex,
	peerID, recipient string,
	userMsg chat.Message,
) {
	peerLog := log.With("peer", peerID)
	start := time.Now()
	peerLog.Debug("handler: start", "bytes_in", len(userMsg.Text), "attachments", len(userMsg.Attachments))

	var reply strings.Builder
	var replyErr error
	chunkCount := 0

	for chunk := range handler(ctx, userMsg) {
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
		peerLog.Warn("handler: empty reply — nothing to send", "duration", dur)
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

	sendStart := time.Now()
	if _, err := cli.call("send", sendParams{
		Recipient: []string{recipient},
		Message:   body,
	}); err != nil {
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
