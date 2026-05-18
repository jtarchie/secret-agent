package cron

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
)

// stubRunner captures prompt-mode invocations so tests can assert on the
// convID and text without wiring a real *runtime.Runtime.
type stubRunner struct {
	mu      sync.Mutex
	convIDs []string
	texts   []string
	reply   string
}

func (s *stubRunner) HandlerFor(convID string) func(context.Context, chat.Message) <-chan chat.Chunk {
	return func(_ context.Context, msg chat.Message) <-chan chat.Chunk {
		s.mu.Lock()
		s.convIDs = append(s.convIDs, convID)
		s.texts = append(s.texts, msg.Text)
		reply := s.reply
		s.mu.Unlock()
		out := make(chan chat.Chunk, 1)
		if reply != "" {
			out <- chat.Chunk{Delta: reply}
		}
		close(out)
		return out
	}
}

func (s *stubRunner) snapshot() (convIDs, texts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.convIDs...), append([]string(nil), s.texts...)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestSchedulerShEveryFires(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "fired")

	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:  "tick",
			Every: "1s",
			Sh:    "echo fire >> " + marker,
		}},
	}

	s := New(discardLogger(), nil)
	err := s.Register(b, &stubRunner{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !s.HasJobs() {
		t.Fatal("expected HasJobs true")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	err = s.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	lines := strings.Count(string(data), "fire")
	if lines < 1 {
		t.Fatalf("expected at least one fire, got %d", lines)
	}
}

func TestSchedulerPromptFires(t *testing.T) {
	runner := &stubRunner{reply: "ok"}
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "ping",
			Every:  "1s",
			Prompt: "say hello",
		}},
	}

	s := New(discardLogger(), nil)
	err := s.Register(b, runner)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err = s.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	convIDs, texts := runner.snapshot()
	if len(convIDs) < 1 {
		t.Fatalf("expected at least one invocation, got %d", len(convIDs))
	}
	wantConv := "cron:tbot:ping"
	if convIDs[0] != wantConv {
		t.Errorf("convID = %q, want %q", convIDs[0], wantConv)
	}
	if texts[0] != "say hello" {
		t.Errorf("text = %q, want %q", texts[0], "say hello")
	}
}

// slowRunner blocks in HandlerFor until the test unblocks it, letting us
// verify SkipIfStillRunning prevents concurrent fires.
type slowRunner struct {
	release chan struct{}
	fires   atomic.Int32
}

func (s *slowRunner) HandlerFor(_ string) func(context.Context, chat.Message) <-chan chat.Chunk {
	return func(_ context.Context, _ chat.Message) <-chan chat.Chunk {
		s.fires.Add(1)
		out := make(chan chat.Chunk)
		go func() {
			<-s.release
			close(out)
		}()
		return out
	}
}

func TestSchedulerSkipsOverlap(t *testing.T) {
	runner := &slowRunner{release: make(chan struct{})}
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "slow",
			Every:  "1s",
			Prompt: "work",
		}},
	}

	s := New(discardLogger(), nil)
	err := s.Register(b, runner)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(runDone)
	}()

	// Wait long enough that multiple ticks have elapsed while the first
	// fire is still held.
	time.Sleep(2200 * time.Millisecond)
	fires := runner.fires.Load()
	close(runner.release)
	<-runDone

	if fires > 1 {
		t.Errorf("expected at most 1 fire while first was running, got %d", fires)
	}
}

func TestSchedulerBadScheduleErrors(t *testing.T) {
	// Bot.Load would normally reject this, but guard the registration path
	// anyway — we don't want a malformed entry to panic the scheduler.
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:     "bad",
			Schedule: "not a cron",
			Sh:       "echo ok",
		}},
	}
	s := New(discardLogger(), nil)
	err := s.Register(b, &stubRunner{})
	if err == nil {
		t.Fatal("expected error")
	}
}

// fakeSender records Send calls for end-to-end sa_send tests.
type fakeSender struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeSender) Send(_ context.Context, to, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, to+"|"+text)
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestSchedulerShDispatchesViaSaSend(t *testing.T) {
	sender := &fakeSender{}
	reg := chat.SenderRegistry{"signal": sender}

	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:  "dispatch",
			Every: "1s",
			Sh:    `sa_send signal +15551234567 "hello from cron"`,
		}},
	}
	s := New(discardLogger(), reg)
	err := s.Register(b, &stubRunner{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err = s.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if sender.count() < 1 {
		t.Fatalf("expected at least one sa_send call, got %d", sender.count())
	}
}

func TestSchedulerExprFires(t *testing.T) {
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:  "compute",
			Every: "1s",
			Expr:  "1 + 1",
		}},
	}
	s := New(discardLogger(), nil)
	err := s.Register(b, &stubRunner{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err = s.Run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestSchedulerHybridShFiresPromptWithOutput(t *testing.T) {
	runner := &stubRunner{reply: "ok"}
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "greet",
			Every:  "1s",
			Sh:     "echo hello",
			Prompt: "got: {{.Output}}",
		}},
	}
	s := New(discardLogger(), nil)
	if err := s.Register(b, runner); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	convIDs, texts := runner.snapshot()
	if len(convIDs) < 1 {
		t.Fatalf("expected at least one invocation, got %d", len(convIDs))
	}
	if convIDs[0] != "cron:tbot:greet" {
		t.Errorf("convID = %q", convIDs[0])
	}
	if !strings.HasPrefix(texts[0], "got: hello") {
		t.Errorf("text = %q, want prefix %q", texts[0], "got: hello")
	}
}

func TestSchedulerHybridShFailureInjectsErr(t *testing.T) {
	runner := &stubRunner{reply: "ok"}
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "boom",
			Every:  "1s",
			Sh:     "exit 7",
			Prompt: "exit={{.ExitCode}} err={{.Err}}",
		}},
	}
	s := New(discardLogger(), nil)
	if err := s.Register(b, runner); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	_, texts := runner.snapshot()
	if len(texts) < 1 {
		t.Fatalf("script failure should not skip turn; got 0 invocations")
	}
	if !strings.Contains(texts[0], "exit=1") {
		t.Errorf("text = %q, want exit=1", texts[0])
	}
	if !strings.Contains(texts[0], "err=") || strings.HasSuffix(texts[0], "err=") {
		t.Errorf("text = %q, want non-empty .Err", texts[0])
	}
}

func TestSchedulerHybridExprFiresPromptWithJSON(t *testing.T) {
	runner := &stubRunner{reply: "ok"}
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "compute",
			Every:  "1s",
			Expr:   "1 + 1",
			Prompt: "value={{.Output}}",
		}},
	}
	s := New(discardLogger(), nil)
	if err := s.Register(b, runner); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	_, texts := runner.snapshot()
	if len(texts) < 1 {
		t.Fatalf("expected at least one invocation")
	}
	if texts[0] != "value=2" {
		t.Errorf("text = %q, want %q", texts[0], "value=2")
	}
}

func TestSchedulerHybridJsFiresPromptWithJSON(t *testing.T) {
	runner := &stubRunner{reply: "ok"}
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "jsfire",
			Every:  "1s",
			Js:     "({a:1, b:2})",
			Prompt: "obj={{.Output}}",
		}},
	}
	s := New(discardLogger(), nil)
	if err := s.Register(b, runner); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	_, texts := runner.snapshot()
	if len(texts) < 1 {
		t.Fatalf("expected at least one invocation")
	}
	if !strings.HasPrefix(texts[0], "obj=") {
		t.Errorf("text = %q, want prefix obj=", texts[0])
	}
	if !strings.Contains(texts[0], `"a":1`) || !strings.Contains(texts[0], `"b":2`) {
		t.Errorf("text = %q, want JSON-marshaled object fields", texts[0])
	}
}

func TestSchedulerHybridTruncatesLargeOutput(t *testing.T) {
	runner := &stubRunner{reply: "ok"}
	// printf %.0sA twice 10000 produces 20000 A's, > MaxScriptOutputBytes.
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "big",
			Every:  "1s",
			Sh:     `head -c 20000 /dev/zero | tr '\0' A`,
			Prompt: "len={{len .Output}}",
		}},
	}
	s := New(discardLogger(), nil)
	if err := s.Register(b, runner); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	_, texts := runner.snapshot()
	if len(texts) < 1 {
		t.Fatalf("expected at least one invocation")
	}
	// {{len .Output}} reports the truncated length: MaxScriptOutputBytes
	// plus the marker bytes.
	if !strings.HasPrefix(texts[0], "len=") {
		t.Fatalf("text = %q", texts[0])
	}
	// Sanity: the rendered length must be greater than the cap (because of
	// the marker) but well under raw 20000.
	if len(texts[0]) > 200 {
		t.Errorf("rendered prompt unexpectedly long: %d bytes", len(texts[0]))
	}
}

func TestSchedulerHybridTemplateRenderErrorSkipsTurn(t *testing.T) {
	runner := &stubRunner{reply: "ok"}
	// Parses fine but .NotAField doesn't exist on scriptResult, so
	// execution fails and the LLM turn should be skipped.
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "bad",
			Every:  "1s",
			Sh:     "echo ok",
			Prompt: "{{.NotAField.X}}",
		}},
	}
	s := New(discardLogger(), nil)
	if err := s.Register(b, runner); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	convIDs, _ := runner.snapshot()
	if len(convIDs) != 0 {
		t.Errorf("template render failure should skip LLM turn, got %d invocations", len(convIDs))
	}
}

func TestSchedulerHybridSendersAvailableToScript(t *testing.T) {
	sender := &fakeSender{}
	reg := chat.SenderRegistry{"signal": sender}
	runner := &stubRunner{reply: "ok"}
	b := &bot.Bot{
		Name: "tbot",
		Cron: []bot.Cron{{
			Name:   "dispatch",
			Every:  "1s",
			Sh:     `sa_send signal +15551234567 "hello from cron" && echo sent`,
			Prompt: "result: {{.Output}}",
		}},
	}
	s := New(discardLogger(), reg)
	if err := s.Register(b, runner); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	if sender.count() < 1 {
		t.Errorf("expected sa_send to be invoked from hybrid sh body")
	}
	_, texts := runner.snapshot()
	if len(texts) < 1 {
		t.Fatalf("expected at least one LLM invocation")
	}
	if !strings.HasPrefix(texts[0], "result: ") {
		t.Errorf("text = %q, want prefix 'result: '", texts[0])
	}
}
