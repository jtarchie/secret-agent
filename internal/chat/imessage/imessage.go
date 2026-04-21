// Package imessage is a chat.Transport backed by the BlueBubbles Server
// (https://bluebubbles.app). The server runs as a macOS app that bridges
// iMessage; we receive new messages via its webhook mechanism and send
// replies via its REST API.
//
// The user is expected to register our webhook URL in the BlueBubbles
// Server UI once; we do not auto-register to avoid creating duplicate
// webhook entries on every startup.
//
// Routing (trigger matching, scope filters, prior-message buffering, and
// per-bot attachment policy) lives in the dispatcher (see internal/router).
package imessage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// Transport is a chat.Transport backed by a BlueBubbles Server.
type Transport struct {
	serverURL     string
	password      string
	stateDir      string
	webhookListen string
	webhookPath   string
	messagePrefix string
	logger        *slog.Logger
	httpClient    *http.Client
}

type Option func(*Transport)

// WithLogger sets a slog.Logger for webhook and REST events.
func WithLogger(l *slog.Logger) Option { return func(t *Transport) { t.logger = l } }

// WithHTTPClient overrides the HTTP client used for REST calls. Useful in
// tests that point it at an httptest.Server.
func WithHTTPClient(c *http.Client) Option {
	return func(t *Transport) { t.httpClient = c }
}

// WithWebhookListen sets the host:port to bind the webhook HTTP listener on.
// Defaults to "127.0.0.1:4321". The BlueBubbles Server must be configured
// to POST events to `http://<this addr>/<webhook_path>` for any delivery to
// reach us.
func WithWebhookListen(addr string) Option {
	return func(t *Transport) { t.webhookListen = addr }
}

// WithWebhookPath sets the URL path the webhook handler is mounted at.
// Defaults to "/imessage-webhook".
func WithWebhookPath(p string) Option {
	return func(t *Transport) { t.webhookPath = p }
}

// WithMessagePrefix prepends a literal string to every outgoing body
// (including "error: ..." replies), matching the Signal/Slack options. On
// iMessage, bot replies otherwise look identical to a human's.
func WithMessagePrefix(p string) Option {
	return func(t *Transport) { t.messagePrefix = p }
}

// New constructs an iMessage transport. serverURL is the BlueBubbles Server
// base URL (e.g. "http://localhost:1234"); password is the server password;
// stateDir is where downloaded attachments are written.
func New(serverURL, password, stateDir string, opts ...Option) *Transport {
	t := &Transport{
		serverURL:     serverURL,
		password:      password,
		stateDir:      stateDir,
		webhookListen: "127.0.0.1:4321",
		webhookPath:   "/imessage-webhook",
		httpClient:    &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.logger == nil {
		t.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return t
}

// Run starts the webhook listener and blocks until ctx is canceled or the
// listener fails.
func (t *Transport) Run(ctx context.Context, dispatcher chat.Dispatcher) error {
	if t.serverURL == "" {
		return errors.New("imessage transport: server URL is required")
	}
	if t.password == "" {
		return errors.New("imessage transport: password is required")
	}
	if t.stateDir == "" {
		return errors.New("imessage transport: state dir is required")
	}

	log := t.logger.With("component", "imessage", "server", t.serverURL, "listen", t.webhookListen, "path", t.webhookPath)

	cli := newClient(t.serverURL, t.password, t.httpClient)

	// Per-conversation send serialization: multi-chunk replies stay ordered
	// within one chat and don't interleave with parallel chats.
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

	mux := http.NewServeMux()
	mux.HandleFunc(t.webhookPath, func(w http.ResponseWriter, r *http.Request) {
		t.handleWebhook(ctx, log, cli, dispatcher, lockFor, w, r)
	})

	srv := &http.Server{
		Addr:              t.webhookListen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", t.webhookListen)
	if err != nil {
		return fmt.Errorf("imessage listen %s: %w", t.webhookListen, err)
	}

	log.Info("listening for BlueBubbles webhooks", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		// ctx is already canceled in this branch; WithoutCancel keeps the
		// parent's values while giving Shutdown real time to drain.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		log.Info("shutdown", "reason", "context canceled", "err", ctx.Err())
		return fmt.Errorf("imessage transport: %w", ctx.Err())
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("imessage webhook server: %w", err)
		}
		return nil
	}
}

// handleWebhook parses one BlueBubbles webhook POST and dispatches it.
// Always responds 200 quickly so BlueBubbles doesn't retry.
func (t *Transport) handleWebhook(
	ctx context.Context,
	log *slog.Logger,
	cli *client,
	dispatcher chat.Dispatcher,
	lockFor func(string) *sync.Mutex,
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var ev webhookEvent
	err := json.NewDecoder(r.Body).Decode(&ev)
	if err != nil {
		log.Warn("decode webhook failed", "err", err)
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	// Ack fast; the real work happens in a goroutine so we don't block
	// BlueBubbles' delivery thread.
	w.WriteHeader(http.StatusOK)

	if ev.Type != "new-message" {
		log.Debug("ignoring non-new-message event", "type", ev.Type)
		return
	}
	if ev.Data.IsFromMe {
		log.Debug("ignoring own-echo message", "guid", ev.Data.GUID)
		return
	}

	text := strings.TrimSpace(ev.Data.Text)
	chat0 := ev.Data.primaryChat()
	if chat0.GUID == "" {
		log.Warn("dropping message with no chat guid", "msg_guid", ev.Data.GUID)
		return
	}

	atts := t.downloadAttachments(ctx, log, cli, ev.Data.GUID, ev.Data.Attachments)
	if text == "" && len(atts) == 0 {
		log.Debug("ignoring empty message", "chat", chat0.GUID)
		return
	}

	env := buildEnvelope(ev.Data)
	log.Info("received message",
		"conv", env.ConvID,
		"kind", env.Kind,
		"sender", env.SenderID,
		"bytes", len(text),
		"attachments", len(atts),
	)

	go t.handleMessage(ctx, log, cli, dispatcher, lockFor(env.ConvID), env, chat.Message{Text: text, Attachments: atts})
}

// handleMessage drains one dispatcher reply stream and sends the resulting
// body back via the REST API, prefixed with MessagePrefix when set.
func (t *Transport) handleMessage(
	ctx context.Context,
	log *slog.Logger,
	cli *client,
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
	err := cli.sendText(ctx, env.ConvID, newTempGUID(), body)
	if err != nil {
		peerLog.Error("send failed", "err", err, "duration", time.Since(sendStart))
		return
	}
	peerLog.Info("send ok", "bytes", len(body), "duration", time.Since(sendStart))
}

// downloadAttachments saves each attachment under stateDir/<msgGUID>/<attGUID>-<name>
// and returns chat.Attachment entries for those that succeeded. Failures are
// logged and skipped.
func (t *Transport) downloadAttachments(ctx context.Context, log *slog.Logger, cli *client, msgGUID string, in []attachment) []chat.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]chat.Attachment, 0, len(in))
	for _, a := range in {
		if a.GUID == "" {
			continue
		}
		dest := filepath.Join(t.stateDir, "attachments", msgGUID, a.GUID+"-"+safeFilename(a.TransferName))
		err := cli.downloadAttachment(ctx, a.GUID, dest)
		if err != nil {
			log.Warn("attachment download failed", "guid", a.GUID, "err", err)
			continue
		}
		out = append(out, chat.Attachment{
			Path:        dest,
			Filename:    a.TransferName,
			ContentType: a.MIMEType,
		})
	}
	return out
}

// buildEnvelope translates a BlueBubbles new-message event into the
// chat.Envelope the dispatcher expects.
func buildEnvelope(d newMessageData) chat.Envelope {
	chat0 := d.primaryChat()
	sender := d.senderAddress()

	env := chat.Envelope{
		ConvID:    chat0.GUID,
		Transport: "imessage",
		SenderID:  sender,
	}
	if e164Re.MatchString(sender) {
		env.SenderPhone = sender
	}
	if d.isGroup() {
		env.Kind = "group"
		env.GroupID = chat0.GUID
	} else {
		env.Kind = "dm"
	}
	return env
}

// newTempGUID is the client-supplied ID BlueBubbles uses to correlate send
// requests with eventual delivery events. It only needs to be unique per
// send; a random hex string is plenty.
func newTempGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "secret-agent-" + hex.EncodeToString(b[:])
}

// safeFilename strips path separators so a hostile filename can't escape
// the per-message attachment dir.
func safeFilename(name string) string {
	if name == "" {
		return "file"
	}
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	if name == "." || name == ".." {
		return "file"
	}
	return name
}

var e164Re = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)
