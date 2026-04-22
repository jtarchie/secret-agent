// Package slack is a chat.Transport backed by Slack's Socket Mode API.
// It receives DMs and channel messages from a Slack workspace, forwards
// each to a chat.Dispatcher along with sender metadata, collects the reply
// stream, and sends a single Slack message back per turn (threaded when
// the prompt was threaded).
//
// Routing (trigger matching, scope filters, prior-message buffering, and
// per-bot attachment policy) lives in the dispatcher (see internal/router).
package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jtarchie/secret-agent/internal/chat"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// Transport is a chat.Transport backed by a slack-go Socket Mode client.
type Transport struct {
	botToken      string
	appToken      string
	messagePrefix string
	logger        *slog.Logger
	httpClient    *http.Client
	// fileDownloadTimeout bounds each attachment download. Slack file URLs
	// are small by default (~a few MB); 60s is a generous ceiling.
	fileDownloadTimeout time.Duration

	// senderMu guards senderAPI. Populated while Run is active so Send can
	// post messages on the same authenticated client.
	senderMu  sync.Mutex
	senderAPI *slackgo.Client
}

type Option func(*Transport)

// WithLogger sets a slog.Logger for socket-mode and internal events.
func WithLogger(l *slog.Logger) Option { return func(t *Transport) { t.logger = l } }

// WithHTTPClient overrides the HTTP client used to download attachments.
// Mainly useful in tests.
func WithHTTPClient(c *http.Client) Option {
	return func(t *Transport) { t.httpClient = c }
}

// WithMessagePrefix prepends a literal string to every outgoing body
// (including "error: ..." replies). Slack already badges bot messages, so
// this is mostly offered for parity with Signal.
func WithMessagePrefix(p string) Option {
	return func(t *Transport) { t.messagePrefix = p }
}

// New returns a Slack transport. botToken is the bot user OAuth token
// (xoxb-…); appToken is the app-level token used to open the Socket Mode
// connection (xapp-…, scope connections:write).
func New(botToken, appToken string, opts ...Option) *Transport {
	t := &Transport{
		botToken:            botToken,
		appToken:            appToken,
		httpClient:          &http.Client{Timeout: 60 * time.Second},
		fileDownloadTimeout: 60 * time.Second,
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.logger == nil {
		t.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return t
}

// Run opens a Socket Mode connection and pumps incoming messages through
// the dispatcher. Returns when the context is canceled or the socket
// connection is terminally lost.
func (t *Transport) Run(ctx context.Context, dispatcher chat.Dispatcher) error {
	if t.botToken == "" {
		return errors.New("slack transport: bot token is required")
	}
	if t.appToken == "" {
		return errors.New("slack transport: app token is required")
	}
	if !strings.HasPrefix(t.botToken, "xoxb-") {
		return errors.New("slack transport: bot token must start with xoxb-")
	}
	if !strings.HasPrefix(t.appToken, "xapp-") {
		return errors.New("slack transport: app token must start with xapp-")
	}

	log := t.logger.With("component", "slack")

	api := slackgo.New(
		t.botToken,
		slackgo.OptionAppLevelToken(t.appToken),
	)
	t.senderMu.Lock()
	t.senderAPI = api
	t.senderMu.Unlock()
	defer func() {
		t.senderMu.Lock()
		t.senderAPI = nil
		t.senderMu.Unlock()
	}()

	authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	auth, err := api.AuthTestContext(authCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("slack auth.test: %w", err)
	}
	botID := auth.BotID
	log = log.With("bot_id", botID, "user_id", auth.UserID, "team", auth.Team)
	log.Info("slack authenticated")

	sm := socketmode.New(api)

	// Per-channel send serialization so multi-chunk replies from one
	// conversation stay ordered and stay under Slack's per-channel rate
	// limit (~1/sec on chat.postMessage).
	var chMuM sync.Mutex
	chMu := map[string]*sync.Mutex{}
	lockFor := func(ch string) *sync.Mutex {
		chMuM.Lock()
		defer chMuM.Unlock()
		mu, ok := chMu[ch]
		if !ok {
			mu = &sync.Mutex{}
			chMu[ch] = mu
		}
		return mu
	}

	// Event pump: runs until Events is closed by RunContext returning.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range sm.Events {
			t.handleEvent(ctx, log, sm, api, dispatcher, lockFor, botID, evt)
		}
	}()

	log.Info("slack socket mode: starting")
	runErr := sm.RunContext(ctx)
	<-done

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("slack socket mode: %w", runErr)
	}
	return nil
}

// Send posts an unsolicited message. `to` may be a user ID (U.../W...),
// a channel ID (C.../G...), or an IM channel ID (D...). chat.postMessage
// accepts user IDs directly and opens the IM conversation on demand.
func (t *Transport) Send(ctx context.Context, to, text string) error {
	t.senderMu.Lock()
	api := t.senderAPI
	t.senderMu.Unlock()
	if api == nil {
		return errors.New("slack transport: not running (cannot send)")
	}
	if to == "" {
		return errors.New("slack send: channel is required")
	}
	body := text
	if t.messagePrefix != "" {
		body = t.messagePrefix + body
	}
	_, _, err := api.PostMessageContext(ctx, to, slackgo.MsgOptionText(body, false))
	if err != nil {
		return fmt.Errorf("slack send: %w", err)
	}
	return nil
}

// handleEvent dispatches one socket-mode event. Non-message events are
// logged and ignored; message events are acked immediately and processed
// asynchronously so the 3-second Slack ack deadline is always met.
func (t *Transport) handleEvent(
	ctx context.Context,
	log *slog.Logger,
	sm *socketmode.Client,
	api *slackgo.Client,
	dispatcher chat.Dispatcher,
	lockFor func(string) *sync.Mutex,
	botID string,
	evt socketmode.Event,
) {
	switch evt.Type { //nolint:exhaustive // we only handle the subset of events we care about
	case socketmode.EventTypeConnecting:
		log.Info("slack socket mode: connecting")
	case socketmode.EventTypeConnected:
		log.Info("slack socket mode: connected")
	case socketmode.EventTypeDisconnect:
		log.Info("slack socket mode: disconnected")
	case socketmode.EventTypeHello:
		log.Debug("slack socket mode: hello")
	case socketmode.EventTypeEventsAPI:
		apiEvt, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			log.Warn("events api: unexpected data shape")
			return
		}
		// Always ack before we start the real work.
		if evt.Request != nil {
			err := sm.AckCtx(ctx, evt.Request.EnvelopeID, nil)
			if err != nil {
				log.Warn("ack failed", "err", err)
			}
		}
		if apiEvt.Type != slackevents.CallbackEvent {
			log.Debug("events api: non-callback event", "type", apiEvt.Type)
			return
		}
		msg, ok := apiEvt.InnerEvent.Data.(*slackevents.MessageEvent)
		if !ok {
			log.Debug("events api: non-message inner event", "type", apiEvt.InnerEvent.Type)
			return
		}
		if ok, reason := shouldDispatch(msg, botID); !ok {
			log.Debug("message dropped", "reason", reason, "user", msg.User, "channel", msg.Channel)
			return
		}
		go t.handleMessage(ctx, log, api, dispatcher, lockFor, msg)
	default:
		log.Debug("slack socket mode: ignoring event", "type", evt.Type)
	}
}

// handleMessage dispatches a single filtered MessageEvent. Attachments are
// downloaded to a per-turn temp dir; the dir is removed after the reply
// send completes so the LLM turn sees the files for the whole run.
func (t *Transport) handleMessage(
	ctx context.Context,
	log *slog.Logger,
	api *slackgo.Client,
	dispatcher chat.Dispatcher,
	lockFor func(string) *sync.Mutex,
	ev *slackevents.MessageEvent,
) {
	convID := convIDFor(ev.Channel, ev.TimeStamp, ev.ThreadTimeStamp)
	peerLog := log.With("conv", convID, "kind", kindFor(ev.ChannelType), "channel", ev.Channel, "user", ev.User)

	files := filesFor(ev)
	var atts []chat.Attachment
	var cleanupDir string
	if len(files) > 0 {
		dir, err := os.MkdirTemp("", "slack-att-*")
		if err != nil {
			peerLog.Error("tempdir failed; proceeding without attachments", "err", err)
		} else {
			cleanupDir = dir
			atts = t.downloadFiles(ctx, peerLog, dir, files)
		}
	}
	defer func() {
		if cleanupDir != "" {
			_ = os.RemoveAll(cleanupDir)
		}
	}()

	text := strings.TrimSpace(ev.Text)
	peerLog.Info("received message", "bytes", len(text), "attachments", len(atts))

	env := buildEnvelope(ev)
	userMsg := chat.Message{Text: text, Attachments: atts}

	var reply strings.Builder
	var replyErr error
	chunkCount := 0
	start := time.Now()
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

	mu := lockFor(ev.Channel)
	mu.Lock()
	defer mu.Unlock()

	opts := []slackgo.MsgOption{
		slackgo.MsgOptionText(body, false),
	}
	if ts := replyTS(ev.TimeStamp, ev.ThreadTimeStamp); ts != "" {
		opts = append(opts, slackgo.MsgOptionTS(ts))
	}
	sendStart := time.Now()
	_, _, err := api.PostMessageContext(ctx, ev.Channel, opts...)
	if err != nil {
		peerLog.Error("send failed", "err", err, "duration", time.Since(sendStart))
		return
	}
	peerLog.Info("send ok", "bytes", len(body), "duration", time.Since(sendStart))
}

// downloadFiles fetches each file reference into dir, returning
// chat.Attachment entries for files that succeeded. Failures are logged
// and skipped so the rest of the turn can still run.
func (t *Transport) downloadFiles(
	ctx context.Context,
	log *slog.Logger,
	dir string,
	files []fileRef,
) []chat.Attachment {
	out := make([]chat.Attachment, 0, len(files))
	for _, f := range files {
		path := filepath.Join(dir, f.ID+"-"+safeFilename(f.Name))
		err := t.downloadOne(ctx, f.DownloadURL, path)
		if err != nil {
			log.Warn("download failed", "file_id", f.ID, "url", f.DownloadURL, "err", err)
			continue
		}
		out = append(out, chat.Attachment{
			Path:        path,
			Filename:    f.Name,
			ContentType: f.ContentType,
		})
	}
	return out
}

func (t *Transport) downloadOne(ctx context.Context, url, dest string) error {
	dlCtx, cancel := context.WithTimeout(ctx, t.fileDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.botToken)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return fmt.Errorf("copy %s: %w", dest, err)
	}
	return nil
}

// safeFilename strips path separators so a hostile filename can't escape
// the per-turn temp dir. Only the basename of user input is used; the ID
// prefix added by the caller keeps sibling filenames from colliding.
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
