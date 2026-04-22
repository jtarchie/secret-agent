// Package imessage is a chat.Transport for macOS iMessage. Reception is
// driven by polling the Messages.app SQLite database at
// ~/Library/Messages/chat.db via the `sqlite3` CLI; sending is done by
// driving Messages.app with `osascript`. Both shell-outs match how the
// notes-bot already integrates with macOS.
//
// The host must grant the secret-agent process two TCC permissions:
//   - Full Disk Access (to read chat.db)
//   - Automation → Messages (to let osascript send messages on your behalf)
//
// Known limitation: on recent macOS releases, message.text in chat.db is
// often NULL and the real payload lives in message.attributedBody as a
// typedstream blob. This transport reads message.text only; messages
// authored with rich content may appear blank and get dropped until
// attributedBody decoding is added.
//
// Routing (trigger matching, scope filters, prior-message buffering, and
// per-bot attachment policy) lives in the dispatcher (see internal/router).
package imessage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// Transport is a chat.Transport that polls chat.db and sends via osascript.
type Transport struct {
	databasePath  string
	stateDir      string
	pollInterval  time.Duration
	sqliteBinary  string
	osascriptBin  string
	messagePrefix string
	logger        *slog.Logger
}

type Option func(*Transport)

// WithLogger sets a slog.Logger for poll and send events.
func WithLogger(l *slog.Logger) Option { return func(t *Transport) { t.logger = l } }

// WithPollInterval sets the chat.db poll cadence. Lower values reduce
// reply latency; higher values reduce CPU wake-ups. Defaults to 2s.
func WithPollInterval(d time.Duration) Option {
	return func(t *Transport) { t.pollInterval = d }
}

// WithSQLiteBinary overrides the sqlite3 binary path. Mainly for tests.
func WithSQLiteBinary(p string) Option { return func(t *Transport) { t.sqliteBinary = p } }

// WithOsascriptBinary overrides the osascript binary path. Mainly for tests.
func WithOsascriptBinary(p string) Option { return func(t *Transport) { t.osascriptBin = p } }

// WithMessagePrefix prepends a literal string to every outgoing body
// (including "error: ..." replies). Matches the Signal/Slack options.
func WithMessagePrefix(p string) Option {
	return func(t *Transport) { t.messagePrefix = p }
}

// New constructs an iMessage transport. databasePath is the chat.db path
// (typically ~/Library/Messages/chat.db); stateDir is where the ROWID
// cursor is persisted across restarts so we don't re-deliver history.
func New(databasePath, stateDir string, opts ...Option) *Transport {
	t := &Transport{
		databasePath: databasePath,
		stateDir:     stateDir,
		pollInterval: 2 * time.Second,
		sqliteBinary: "sqlite3",
		osascriptBin: "osascript",
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.logger == nil {
		t.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return t
}

// Run polls chat.db on t.pollInterval and dispatches each new message.
// Returns when ctx is canceled or an unrecoverable error occurs.
func (t *Transport) Run(ctx context.Context, dispatcher chat.Dispatcher) error {
	if t.databasePath == "" {
		return errors.New("imessage transport: database path is required")
	}
	if t.stateDir == "" {
		return errors.New("imessage transport: state dir is required")
	}

	log := t.logger.With("component", "imessage", "database", t.databasePath)

	err := os.MkdirAll(t.stateDir, 0o700)
	if err != nil {
		return fmt.Errorf("imessage state dir: %w", err)
	}
	cursorPath := filepath.Join(t.stateDir, "cursor")

	cursor, err := loadCursor(cursorPath)
	if err != nil {
		return fmt.Errorf("imessage cursor: %w", err)
	}
	// On first start (no cursor persisted), skip existing history by
	// seeding the cursor to the current MAX(ROWID). Otherwise the first
	// poll would replay every message in the database.
	if cursor == 0 {
		max, err := fetchMaxROWID(ctx, t.sqliteBinary, t.databasePath)
		if err != nil {
			return fmt.Errorf("imessage initial max rowid: %w", err)
		}
		cursor = max
		err = saveCursor(cursorPath, cursor)
		if err != nil {
			return fmt.Errorf("imessage save cursor: %w", err)
		}
		log.Info("seeded cursor to current max rowid", "cursor", cursor)
	}

	var convMuM sync.Mutex
	convMu := map[string]*sync.Mutex{}
	lockFor := func(convID string) *sync.Mutex {
		convMuM.Lock()
		defer convMuM.Unlock()
		mu, ok := convMu[convID]
		if !ok {
			mu = &sync.Mutex{}
			convMu[convID] = mu
		}
		return mu
	}

	log.Info("polling chat.db", "interval", t.pollInterval, "cursor", cursor)

	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("shutdown", "reason", "context canceled", "err", ctx.Err())
			return fmt.Errorf("imessage transport: %w", ctx.Err())
		case <-ticker.C:
			newCursor, err := t.pollOnce(ctx, log, dispatcher, cursor, lockFor)
			if err != nil {
				// Don't die on transient DB errors (e.g. the file was briefly
				// locked by Messages.app during a write). Log and retry on
				// the next tick.
				log.Warn("poll failed, will retry", "err", err)
				continue
			}
			if newCursor != cursor {
				cursor = newCursor
				err := saveCursor(cursorPath, cursor)
				if err != nil {
					log.Warn("cursor persist failed", "err", err, "cursor", cursor)
				}
			}
		}
	}
}

// pollOnce fetches all rows past `cursor`, dispatches the ones that pass
// the filter, and returns the new cursor (max ROWID seen, or the old
// cursor if no rows came back).
func (t *Transport) pollOnce(
	ctx context.Context,
	log *slog.Logger,
	dispatcher chat.Dispatcher,
	cursor int64,
	lockFor func(string) *sync.Mutex,
) (int64, error) {
	rows, err := fetchNewMessages(ctx, t.sqliteBinary, t.databasePath, cursor)
	if err != nil {
		return cursor, err
	}
	for _, r := range rows {
		if r.ROWID > cursor {
			cursor = r.ROWID
		}
		t.handleRow(ctx, log, dispatcher, lockFor, r)
	}
	return cursor, nil
}

// handleRow filters one chat.db row and, if it's a real inbound user
// message, dispatches it and sends the reply.
func (t *Transport) handleRow(
	ctx context.Context,
	log *slog.Logger,
	dispatcher chat.Dispatcher,
	lockFor func(string) *sync.Mutex,
	r row,
) {
	if r.IsFromMe == 1 {
		log.Debug("skip own message", "rowid", r.ROWID)
		return
	}
	if r.ChatGUID == "" {
		log.Debug("skip message with no chat", "rowid", r.ROWID)
		return
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		log.Debug("skip empty message (text may live in attributedBody)",
			"rowid", r.ROWID, "msg_guid", r.MsgGUID,
		)
		return
	}

	env := buildEnvelope(r)
	log.Info("received message",
		"rowid", r.ROWID,
		"conv", env.ConvID,
		"kind", env.Kind,
		"sender", env.SenderID,
		"bytes", len(text),
	)

	go t.handleMessage(ctx, log, dispatcher, lockFor(env.ConvID), env, chat.Message{Text: text})
}

// handleMessage drains the dispatcher reply stream and hands the result to
// osascript for delivery.
func (t *Transport) handleMessage(
	ctx context.Context,
	log *slog.Logger,
	dispatcher chat.Dispatcher,
	convLock *sync.Mutex,
	env chat.Envelope,
	userMsg chat.Message,
) {
	peerLog := log.With("conv", env.ConvID, "kind", env.Kind)
	start := time.Now()

	var reply strings.Builder
	var replyErr error
	chunkCount := 0
	for chunk := range dispatcher.Dispatch(ctx, env, userMsg) {
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
		peerLog.Error("handler failed", "err", replyErr, "duration", dur)
		body = "error: " + replyErr.Error()
	} else if body == "" {
		peerLog.Debug("empty reply — nothing to send", "duration", dur)
		return
	} else {
		peerLog.Info("handler done", "bytes_out", len(body), "chunks", chunkCount, "duration", dur)
	}

	if t.messagePrefix != "" {
		body = t.messagePrefix + body
	}

	convLock.Lock()
	defer convLock.Unlock()

	sendStart := time.Now()
	err := t.send(ctx, env, body)
	if err != nil {
		peerLog.Error("send failed", "err", err, "duration", time.Since(sendStart))
		return
	}
	peerLog.Info("send ok", "bytes", len(body), "duration", time.Since(sendStart))
}

// send dispatches to the right AppleScript send form based on env.Kind.
// DMs address the buddy directly (works for either phone or email senders);
// groups address the chat by GUID so replies land in the right thread.
func (t *Transport) send(ctx context.Context, env chat.Envelope, body string) error {
	if env.Kind == "group" {
		return sendGroup(ctx, t.osascriptBin, env.GroupID, body)
	}
	if env.SenderID == "" {
		return errors.New("no sender id for dm reply")
	}
	return sendDM(ctx, t.osascriptBin, env.SenderID, body)
}

// buildEnvelope translates one chat.db row into the chat.Envelope the
// dispatcher expects.
func buildEnvelope(r row) chat.Envelope {
	env := chat.Envelope{
		ConvID:    r.ChatGUID,
		Transport: "imessage",
		SenderID:  r.SenderAddress,
	}
	if e164Re.MatchString(r.SenderAddress) {
		env.SenderPhone = r.SenderAddress
	}
	if styleIsGroup(r.ChatStyle) || r.ParticipantCount > 1 {
		env.Kind = "group"
		env.GroupID = r.ChatGUID
	} else {
		env.Kind = "dm"
	}
	return env
}

var e164Re = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// loadCursor reads the persisted max-ROWID from disk. Missing file → 0,
// which the caller treats as "seed from current MAX(ROWID)".
func loadCursor(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read cursor: %w", err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, nil
	}
	var n int64
	_, err = fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return 0, fmt.Errorf("parse cursor %q: %w", s, err)
	}
	return n, nil
}

func saveCursor(path string, n int64) error {
	err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", n)), 0o600)
	if err != nil {
		return fmt.Errorf("write cursor: %w", err)
	}
	return nil
}
