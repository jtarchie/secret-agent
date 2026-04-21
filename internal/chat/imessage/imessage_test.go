package imessage

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtarchie/secret-agent/internal/chat"
)

func nullLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeDispatcher records the envelope/message received and replies with a
// canned body on a goroutine-managed channel.
type fakeDispatcher struct {
	mu    sync.Mutex
	calls []dispatchCall
	reply string
}

type dispatchCall struct {
	env chat.Envelope
	msg chat.Message
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, env chat.Envelope, msg chat.Message) <-chan chat.Chunk {
	f.mu.Lock()
	f.calls = append(f.calls, dispatchCall{env: env, msg: msg})
	reply := f.reply
	f.mu.Unlock()

	ch := make(chan chat.Chunk, 1)
	if reply != "" {
		ch <- chat.Chunk{Delta: reply}
	}
	close(ch)
	return ch
}

func (f *fakeDispatcher) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.calls)
		f.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("dispatcher received %d calls, want %d", len(f.calls), n)
}

func TestBuildEnvelopeDM(t *testing.T) {
	d := newMessageData{
		GUID:   "msg-1",
		Text:   "hi",
		Handle: &handle{Address: "+15551234567"},
		Chats: []chatRef{{
			GUID:         "any;-;+15551234567",
			Participants: []participant{{Address: "+15551234567"}},
		}},
	}
	env := buildEnvelope(d)
	if env.Transport != "imessage" {
		t.Errorf("Transport = %q, want imessage", env.Transport)
	}
	if env.Kind != "dm" {
		t.Errorf("Kind = %q, want dm", env.Kind)
	}
	if env.ConvID != "any;-;+15551234567" {
		t.Errorf("ConvID = %q", env.ConvID)
	}
	if env.SenderID != "+15551234567" {
		t.Errorf("SenderID = %q", env.SenderID)
	}
	if env.SenderPhone != "+15551234567" {
		t.Errorf("SenderPhone = %q, want E.164", env.SenderPhone)
	}
	if env.GroupID != "" {
		t.Errorf("GroupID = %q, want empty for DM", env.GroupID)
	}
}

func TestBuildEnvelopeGroup(t *testing.T) {
	d := newMessageData{
		Handle: &handle{Address: "+15551234567"},
		Chats: []chatRef{{
			GUID: "iMessage;+;chat123",
			Participants: []participant{
				{Address: "+15551234567"},
				{Address: "+15559876543"},
				{Address: "other@example.com"},
			},
		}},
	}
	env := buildEnvelope(d)
	if env.Kind != "group" {
		t.Errorf("Kind = %q, want group", env.Kind)
	}
	if env.GroupID != "iMessage;+;chat123" || env.ConvID != "iMessage;+;chat123" {
		t.Errorf("GroupID/ConvID = %q/%q", env.GroupID, env.ConvID)
	}
}

func TestBuildEnvelopeEmailSenderLeavesPhoneEmpty(t *testing.T) {
	d := newMessageData{
		Handle: &handle{Address: "person@icloud.com"},
		Chats: []chatRef{{
			GUID:         "iMessage;-;person@icloud.com",
			Participants: []participant{{Address: "person@icloud.com"}},
		}},
	}
	env := buildEnvelope(d)
	if env.SenderID != "person@icloud.com" {
		t.Errorf("SenderID = %q", env.SenderID)
	}
	if env.SenderPhone != "" {
		t.Errorf("SenderPhone should be empty for email senders, got %q", env.SenderPhone)
	}
}

func TestClientSendTextHitsExpectedEndpoint(t *testing.T) {
	var gotMethod, gotPath, gotPassword, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotPassword = r.URL.Query().Get("password")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cli := newClient(srv.URL, "pw!with space", srv.Client())
	err := cli.sendText(context.Background(), "any;-;+15551234567", "temp-xyz", "hello")
	if err != nil {
		t.Fatalf("sendText: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q", gotMethod)
	}
	if gotPath != "/api/v1/message/text" {
		t.Errorf("path = %q", gotPath)
	}
	if gotPassword != "pw!with space" {
		t.Errorf("password query = %q (should be URL-decoded to original)", gotPassword)
	}
	var decoded map[string]string
	err = json.Unmarshal([]byte(gotBody), &decoded)
	if err != nil {
		t.Fatalf("body not json: %q", gotBody)
	}
	if decoded["chatGuid"] != "any;-;+15551234567" || decoded["tempGuid"] != "temp-xyz" || decoded["message"] != "hello" {
		t.Errorf("body fields wrong: %+v", decoded)
	}
}

func TestClientSendTextReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("chat not found"))
	}))
	defer srv.Close()

	cli := newClient(srv.URL, "pw", srv.Client())
	err := cli.sendText(context.Background(), "bad-chat", "t", "x")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error should name status + body, got: %v", err)
	}
}

func TestClientDownloadAttachmentWritesFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/attachment/") {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "sub", "photo.png")
	cli := newClient(srv.URL, "pw", srv.Client())
	err := cli.downloadAttachment(context.Background(), "ATT-1", dest)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "PNGDATA" {
		t.Errorf("file = %q", string(got))
	}
}

// TestWebhookHandlerDispatchesNewMessage runs a full round-trip: post a
// webhook payload, wait for the dispatcher to see it, and confirm the
// reply was sent back via the REST endpoint.
func TestWebhookHandlerDispatchesNewMessage(t *testing.T) {
	var sendBody bytes.Buffer
	sendHits := make(chan struct{}, 1)
	bb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/message/text" {
			_, _ = io.Copy(&sendBody, r.Body)
			w.WriteHeader(http.StatusOK)
			sendHits <- struct{}{}
			return
		}
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
	}))
	defer bb.Close()

	disp := &fakeDispatcher{reply: "pong"}

	tr := New(bb.URL, "pw", t.TempDir(),
		WithLogger(nullLogger()),
		WithHTTPClient(bb.Client()),
		WithWebhookListen("127.0.0.1:0"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- tr.Run(ctx, disp) }()

	// Give the listener a moment to bind. Use a httptest.Server bound to the
	// same handler as an easier end-run: call handleWebhook directly with a
	// recorded request instead of racing the real port.
	cli := newClient(bb.URL, "pw", bb.Client())
	lockFor := func(string) *sync.Mutex { return &sync.Mutex{} }

	payload := webhookEvent{
		Type: "new-message",
		Data: newMessageData{
			GUID:   "msg-1",
			Text:   "ping",
			Handle: &handle{Address: "+15551234567"},
			Chats: []chatRef{{
				GUID:         "any;-;+15551234567",
				Participants: []participant{{Address: "+15551234567"}},
			}},
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/imessage-webhook", bytes.NewReader(raw))
	rr := httptest.NewRecorder()

	tr.handleWebhook(ctx, nullLogger(), cli, disp, lockFor, rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("webhook response: %d", rr.Code)
	}

	disp.waitFor(t, 1)

	select {
	case <-sendHits:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected send to BlueBubbles REST, none arrived")
	}

	var got map[string]string
	err := json.Unmarshal(sendBody.Bytes(), &got)
	if err != nil {
		t.Fatalf("send body not json: %q", sendBody.String())
	}
	if got["chatGuid"] != "any;-;+15551234567" || got["message"] != "pong" {
		t.Errorf("send body wrong: %+v", got)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not exit after cancel")
	}
}

func TestWebhookHandlerIgnoresIsFromMe(t *testing.T) {
	disp := &fakeDispatcher{reply: "nope"}
	tr := New("http://ignored", "pw", t.TempDir(), WithLogger(nullLogger()))

	payload := webhookEvent{
		Type: "new-message",
		Data: newMessageData{
			GUID:     "msg-echo",
			Text:     "my own reply",
			IsFromMe: true,
			Handle:   &handle{Address: "+15551234567"},
			Chats:    []chatRef{{GUID: "any;-;+15551234567"}},
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/imessage-webhook", bytes.NewReader(raw))
	rr := httptest.NewRecorder()

	tr.handleWebhook(context.Background(), nullLogger(), nil, disp, func(string) *sync.Mutex { return &sync.Mutex{} }, rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("should ack anyway, got %d", rr.Code)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.calls) != 0 {
		t.Errorf("isFromMe event should not reach dispatcher: got %d calls", len(disp.calls))
	}
}

func TestWebhookHandlerRejectsNonPost(t *testing.T) {
	tr := New("http://ignored", "pw", t.TempDir(), WithLogger(nullLogger()))
	disp := &fakeDispatcher{}

	req := httptest.NewRequest(http.MethodGet, "/imessage-webhook", nil)
	rr := httptest.NewRecorder()
	tr.handleWebhook(context.Background(), nullLogger(), nil, disp, func(string) *sync.Mutex { return &sync.Mutex{} }, rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", rr.Code)
	}
}

func TestWebhookHandlerDropsEmptyMessage(t *testing.T) {
	disp := &fakeDispatcher{}
	tr := New("http://ignored", "pw", t.TempDir(), WithLogger(nullLogger()))

	payload := webhookEvent{
		Type: "new-message",
		Data: newMessageData{
			GUID:   "msg-empty",
			Text:   "   ",
			Handle: &handle{Address: "+15551234567"},
			Chats:  []chatRef{{GUID: "any;-;+15551234567"}},
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/imessage-webhook", bytes.NewReader(raw))
	rr := httptest.NewRecorder()

	tr.handleWebhook(context.Background(), nullLogger(), nil, disp, func(string) *sync.Mutex { return &sync.Mutex{} }, rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("should ack anyway, got %d", rr.Code)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.calls) != 0 {
		t.Errorf("empty message should not reach dispatcher: got %d calls", len(disp.calls))
	}
}

func TestWebhookHandlerAppliesMessagePrefix(t *testing.T) {
	var sendBody bytes.Buffer
	sendHits := make(chan struct{}, 1)
	bb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(&sendBody, r.Body)
		w.WriteHeader(http.StatusOK)
		sendHits <- struct{}{}
	}))
	defer bb.Close()

	disp := &fakeDispatcher{reply: "hello"}
	tr := New(bb.URL, "pw", t.TempDir(),
		WithLogger(nullLogger()),
		WithHTTPClient(bb.Client()),
		WithMessagePrefix("[bot] "),
	)
	cli := newClient(bb.URL, "pw", bb.Client())

	payload := webhookEvent{
		Type: "new-message",
		Data: newMessageData{
			GUID:   "msg-p",
			Text:   "hi",
			Handle: &handle{Address: "+15551234567"},
			Chats: []chatRef{{
				GUID:         "any;-;+15551234567",
				Participants: []participant{{Address: "+15551234567"}},
			}},
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/imessage-webhook", bytes.NewReader(raw))
	rr := httptest.NewRecorder()
	tr.handleWebhook(context.Background(), nullLogger(), cli, disp, func(string) *sync.Mutex { return &sync.Mutex{} }, rr, req)

	select {
	case <-sendHits:
	case <-time.After(2 * time.Second):
		t.Fatal("expected send, none arrived")
	}
	var got map[string]string
	err := json.Unmarshal(sendBody.Bytes(), &got)
	if err != nil {
		t.Fatalf("send body not json: %q", sendBody.String())
	}
	if got["message"] != "[bot] hello" {
		t.Errorf("message = %q, want prefix applied", got["message"])
	}
}
