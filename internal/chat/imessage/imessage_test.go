package imessage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jtarchie/secret-agent/internal/chat"
)

func nullLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// writeScript writes a small POSIX shell script to a temp file and returns
// its path. Used to stand in for the sqlite3 / osascript binaries in tests.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stubs assume a POSIX shell")
	}
	p := filepath.Join(t.TempDir(), "stub.sh")
	err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700)
	if err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return p
}

// fakeDispatcher captures the envelopes it sees and replies with a canned
// body on a per-call buffered channel.
type fakeDispatcher struct {
	mu    sync.Mutex
	calls []dispatchCall
	reply string
}

type dispatchCall struct {
	env chat.Envelope
	msg chat.Message
}

func (f *fakeDispatcher) Dispatch(_ context.Context, env chat.Envelope, msg chat.Message) <-chan chat.Chunk {
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
	r := row{
		ROWID:            42,
		MsgGUID:          "msg-1",
		Text:             "hi",
		SenderAddress:    "+15551234567",
		ChatGUID:         "any;-;+15551234567",
		ChatStyle:        45,
		ParticipantCount: 1,
	}
	env := buildEnvelope(r)
	if env.Transport != "imessage" {
		t.Errorf("Transport = %q", env.Transport)
	}
	if env.Kind != "dm" {
		t.Errorf("Kind = %q, want dm", env.Kind)
	}
	if env.ConvID != "any;-;+15551234567" {
		t.Errorf("ConvID = %q", env.ConvID)
	}
	if env.SenderID != "+15551234567" || env.SenderPhone != "+15551234567" {
		t.Errorf("sender fields = %+v", env)
	}
	if env.GroupID != "" {
		t.Errorf("GroupID should be empty for DM: %q", env.GroupID)
	}
}

func TestBuildEnvelopeGroupByStyle(t *testing.T) {
	r := row{
		ChatGUID:         "iMessage;+;chat123",
		ChatStyle:        43,
		ParticipantCount: 1,
		SenderAddress:    "+15551234567",
	}
	env := buildEnvelope(r)
	if env.Kind != "group" || env.GroupID != "iMessage;+;chat123" {
		t.Errorf("expected group envelope, got %+v", env)
	}
}

func TestBuildEnvelopeGroupByParticipantCount(t *testing.T) {
	// Style is DM-ish but participant count > 1 — still classify as group.
	r := row{
		ChatGUID:         "iMessage;+;chat999",
		ChatStyle:        0,
		ParticipantCount: 3,
		SenderAddress:    "+15551234567",
	}
	env := buildEnvelope(r)
	if env.Kind != "group" {
		t.Errorf("expected group (participants>1), got %q", env.Kind)
	}
}

func TestBuildEnvelopeEmailSenderLeavesPhoneEmpty(t *testing.T) {
	r := row{
		ChatGUID:      "iMessage;-;person@icloud.com",
		SenderAddress: "person@icloud.com",
		ChatStyle:     45,
	}
	env := buildEnvelope(r)
	if env.SenderID != "person@icloud.com" {
		t.Errorf("SenderID = %q", env.SenderID)
	}
	if env.SenderPhone != "" {
		t.Errorf("SenderPhone should be empty for email, got %q", env.SenderPhone)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cursor")
	err := saveCursor(p, 12345)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadCursor(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != 12345 {
		t.Errorf("roundtrip: got %d", got)
	}
}

func TestLoadCursorMissingFileIsZero(t *testing.T) {
	got, err := loadCursor(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != 0 {
		t.Errorf("missing cursor should be 0, got %d", got)
	}
}

func TestStyleIsGroup(t *testing.T) {
	cases := map[int]bool{43: true, 45: false, 0: false}
	for style, want := range cases {
		if got := styleIsGroup(style); got != want {
			t.Errorf("styleIsGroup(%d) = %v, want %v", style, got, want)
		}
	}
}

// sqliteStubEmitting returns a shell-script path that emits the given JSON
// on stdout when invoked — regardless of arguments.
func sqliteStubEmitting(t *testing.T, rows []row) string {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal fixtures: %v", err)
	}
	// Shell-quote the JSON by wrapping in single quotes and escaping any
	// embedded single quotes. Simple approach is fine for test fixtures.
	body := fmt.Sprintf("cat <<'EOF'\n%s\nEOF", string(raw))
	return writeScript(t, body)
}

// osascriptStubRecording returns a shell-script path that, when invoked,
// appends a line describing its arguments to a log file. The log path is
// returned alongside the script path.
func osascriptStubRecording(t *testing.T) (scriptPath, logPath string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "osascript.log")
	// Write the full argv (quoted) plus a blank line; mixes cleanly with
	// downstream string checks.
	body := `echo "args: $@" >> ` + log + `
while IFS= read -r line; do echo "stdin: $line" >> ` + log + `; done`
	return writeScript(t, body), log
}

func TestPollOnceDispatchesAndSends(t *testing.T) {
	rows := []row{{
		ROWID:            101,
		MsgGUID:          "msg-a",
		Text:             "ping",
		IsFromMe:         0,
		SenderAddress:    "+15551234567",
		ChatGUID:         "any;-;+15551234567",
		ChatStyle:        45,
		ParticipantCount: 1,
	}}
	sqlite := sqliteStubEmitting(t, rows)
	osa, osaLog := osascriptStubRecording(t)

	disp := &fakeDispatcher{reply: "pong"}
	tr := New("/ignored/chat.db", t.TempDir(),
		WithLogger(nullLogger()),
		WithSQLiteBinary(sqlite),
		WithOsascriptBinary(osa),
	)

	lockFor := func(string) *sync.Mutex { return &sync.Mutex{} }

	newCursor, err := tr.pollOnce(context.Background(), nullLogger(), disp, 0, lockFor)
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if newCursor != 101 {
		t.Errorf("cursor = %d, want 101", newCursor)
	}
	disp.waitFor(t, 1)

	// Give the send goroutine a moment to shell out.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(osaLog)
		if strings.Contains(string(b), "+15551234567") && strings.Contains(string(b), "pong") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(osaLog)
	t.Fatalf("osascript log missing expected recipient/body. Got: %s", b)
}

func TestPollOnceSkipsIsFromMe(t *testing.T) {
	rows := []row{{
		ROWID:         202,
		Text:          "self-reply",
		IsFromMe:      1,
		SenderAddress: "",
		ChatGUID:      "any;-;+15551234567",
	}}
	sqlite := sqliteStubEmitting(t, rows)
	osa, _ := osascriptStubRecording(t)

	disp := &fakeDispatcher{}
	tr := New("/ignored/chat.db", t.TempDir(),
		WithLogger(nullLogger()),
		WithSQLiteBinary(sqlite),
		WithOsascriptBinary(osa),
	)
	lockFor := func(string) *sync.Mutex { return &sync.Mutex{} }

	newCursor, err := tr.pollOnce(context.Background(), nullLogger(), disp, 0, lockFor)
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if newCursor != 202 {
		t.Errorf("cursor should still advance past skipped rows: %d", newCursor)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.calls) != 0 {
		t.Errorf("isFromMe=1 should not dispatch: got %d calls", len(disp.calls))
	}
}

func TestPollOnceSkipsEmptyText(t *testing.T) {
	rows := []row{{
		ROWID:         303,
		Text:          "   ",
		IsFromMe:      0,
		SenderAddress: "+15551234567",
		ChatGUID:      "any;-;+15551234567",
		ChatStyle:     45,
	}}
	sqlite := sqliteStubEmitting(t, rows)
	osa, _ := osascriptStubRecording(t)

	disp := &fakeDispatcher{}
	tr := New("/ignored/chat.db", t.TempDir(),
		WithLogger(nullLogger()),
		WithSQLiteBinary(sqlite),
		WithOsascriptBinary(osa),
	)
	lockFor := func(string) *sync.Mutex { return &sync.Mutex{} }

	_, err := tr.pollOnce(context.Background(), nullLogger(), disp, 0, lockFor)
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.calls) != 0 {
		t.Errorf("empty text should not dispatch: got %d calls", len(disp.calls))
	}
}

func TestMessagePrefixAppliedOnSend(t *testing.T) {
	rows := []row{{
		ROWID:            404,
		Text:             "hi",
		SenderAddress:    "+15551234567",
		ChatGUID:         "any;-;+15551234567",
		ChatStyle:        45,
		ParticipantCount: 1,
	}}
	sqlite := sqliteStubEmitting(t, rows)
	osa, osaLog := osascriptStubRecording(t)

	disp := &fakeDispatcher{reply: "hello"}
	tr := New("/ignored/chat.db", t.TempDir(),
		WithLogger(nullLogger()),
		WithSQLiteBinary(sqlite),
		WithOsascriptBinary(osa),
		WithMessagePrefix("[bot] "),
	)
	lockFor := func(string) *sync.Mutex { return &sync.Mutex{} }

	_, err := tr.pollOnce(context.Background(), nullLogger(), disp, 0, lockFor)
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(osaLog)
		if strings.Contains(string(b), "[bot] hello") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(osaLog)
	t.Fatalf("prefix not applied. osascript log: %s", b)
}

func TestRunSeedsCursorOnFirstStart(t *testing.T) {
	// sqlite3 stub: branch on the query. When asked for MAX(ROWID), emit
	// [{"max_rowid": 999}]. Any other query returns []. This proves Run
	// seeds the cursor to the current max and never replays history on
	// fresh installs.
	script := `
case "$*" in
  *"MAX(ROWID)"*) cat <<'EOF'
[{"max_rowid":999}]
EOF
  ;;
  *) echo "[]" ;;
esac
`
	sqlite := writeScript(t, script)
	osa, _ := osascriptStubRecording(t)

	disp := &fakeDispatcher{}
	stateDir := t.TempDir()
	tr := New("/ignored/chat.db", stateDir,
		WithLogger(nullLogger()),
		WithSQLiteBinary(sqlite),
		WithOsascriptBinary(osa),
		WithPollInterval(50*time.Millisecond),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = tr.Run(ctx, disp)

	persisted, err := loadCursor(filepath.Join(stateDir, "cursor"))
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if persisted != 999 {
		t.Errorf("expected cursor seeded to 999, got %d", persisted)
	}
	// No dispatch should have happened: MAX seeds the cursor, subsequent
	// polls return empty, and Run exits on context timeout.
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.calls) != 0 {
		t.Errorf("fresh-install Run should not replay history: got %d calls", len(disp.calls))
	}
}
