package router

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingHandler captures the last message it received so assertions can
// inspect what the router forwarded to a bot.
type recordingHandler struct {
	mu         sync.Mutex
	lastConv   string
	lastMsg    chat.Message
	calls      int
	replyText  string
}

func (r *recordingHandler) factory() HandlerFactory {
	return func(convID string) Handler {
		return func(ctx context.Context, msg chat.Message) <-chan chat.Chunk {
			r.mu.Lock()
			r.lastConv = convID
			r.lastMsg = msg
			r.calls++
			reply := r.replyText
			r.mu.Unlock()

			ch := make(chan chat.Chunk, 1)
			if reply != "" {
				ch <- chat.Chunk{Delta: reply}
			}
			close(ch)
			return ch
		}
	}
}

func (r *recordingHandler) snapshot() (string, chat.Message, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastConv, r.lastMsg, r.calls
}

func routeFromInline(t *testing.T, b *bot.Bot, h *recordingHandler) Route {
	t.Helper()
	r, err := RouteFromBot(b, h.factory())
	if err != nil {
		t.Fatalf("RouteFromBot %q: %v", b.Name, err)
	}
	return r
}

func drain(ch <-chan chat.Chunk) string {
	var b strings.Builder
	for c := range ch {
		b.WriteString(c.Delta)
	}
	return b.String()
}

func TestRouterNewRejectsTriggerConflicts(t *testing.T) {
	admin := &bot.Bot{Name: "admin", Triggers: []string{"@bot"}}
	public := &bot.Bot{Name: "public", Triggers: []string{"@BOT"}}

	_, err := New([]Route{
		routeFromInline(t, admin, &recordingHandler{}),
		routeFromInline(t, public, &recordingHandler{}),
	}, WithLogger(nullLogger()))
	if err == nil {
		t.Fatal("expected conflict error for overlapping triggers")
	}
	if !strings.Contains(err.Error(), "@bot") ||
		!strings.Contains(err.Error(), "admin") ||
		!strings.Contains(err.Error(), "public") {
		t.Errorf("error should name trigger and both bots: %v", err)
	}
}

func TestRouterNewRequiresTriggersInMultiBotMode(t *testing.T) {
	admin := &bot.Bot{Name: "admin", Triggers: []string{"@admin"}}
	public := &bot.Bot{Name: "public"}

	_, err := New([]Route{
		routeFromInline(t, admin, &recordingHandler{}),
		routeFromInline(t, public, &recordingHandler{}),
	}, WithLogger(nullLogger()))
	if err == nil {
		t.Fatal("expected error when a bot has no triggers in multi-bot mode")
	}
	if !strings.Contains(err.Error(), "public") {
		t.Errorf("error should name the offending bot: %v", err)
	}
}

func TestRouterSingleBotAllowsNoTriggers(t *testing.T) {
	only := &bot.Bot{Name: "only"}
	h := &recordingHandler{replyText: "hi"}

	rtr, err := New([]Route{routeFromInline(t, only, h)}, WithLogger(nullLogger()))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	out := drain(rtr.Dispatch(context.Background(),
		chat.Envelope{ConvID: "c", Kind: "dm", SenderPhone: "+15550000000"},
		chat.Message{Text: "anything"},
	))
	if out != "hi" {
		t.Errorf("got %q, want hi (single-bot should always match)", out)
	}
}

func TestRouterSelectsByTriggerAcrossBots(t *testing.T) {
	admin := &bot.Bot{Name: "admin", Triggers: []string{"@admin"}}
	public := &bot.Bot{Name: "public", Triggers: []string{"@bot"}}

	hA := &recordingHandler{replyText: "A"}
	hP := &recordingHandler{replyText: "P"}

	rtr, err := New([]Route{
		routeFromInline(t, admin, hA),
		routeFromInline(t, public, hP),
	}, WithLogger(nullLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	env := chat.Envelope{ConvID: "c", Kind: "dm", SenderPhone: "+15550000000"}

	if out := drain(rtr.Dispatch(context.Background(), env, chat.Message{Text: "hey @bot"})); out != "P" {
		t.Errorf("@bot routed to %q, want P", out)
	}
	if out := drain(rtr.Dispatch(context.Background(), env, chat.Message{Text: "yo @admin"})); out != "A" {
		t.Errorf("@admin routed to %q, want A", out)
	}
}

func TestRouterUserScopeFiltering(t *testing.T) {
	admin := &bot.Bot{
		Name:     "admin",
		Triggers: []string{"@bot"},
		Users:    []string{"+15551111111"},
	}
	public := &bot.Bot{
		Name:     "public",
		Triggers: []string{"@hi"},
	}

	hA := &recordingHandler{replyText: "A"}
	hP := &recordingHandler{replyText: "P"}

	rtr, err := New([]Route{
		routeFromInline(t, admin, hA),
		routeFromInline(t, public, hP),
	}, WithLogger(nullLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Allowlisted user saying @bot → admin handles.
	out := drain(rtr.Dispatch(context.Background(),
		chat.Envelope{ConvID: "c1", Kind: "dm", SenderPhone: "+15551111111"},
		chat.Message{Text: "@bot help"},
	))
	if out != "A" {
		t.Errorf("allowlisted @bot routed to %q, want A", out)
	}

	// Non-allowlisted user saying @bot → no route matches → silent.
	out = drain(rtr.Dispatch(context.Background(),
		chat.Envelope{ConvID: "c2", Kind: "dm", SenderPhone: "+15559999999"},
		chat.Message{Text: "@bot help"},
	))
	if out != "" {
		t.Errorf("non-allowlisted @bot should be silent, got %q", out)
	}

	// Non-allowlisted user saying @hi → public handles.
	out = drain(rtr.Dispatch(context.Background(),
		chat.Envelope{ConvID: "c2", Kind: "dm", SenderPhone: "+15559999999"},
		chat.Message{Text: "@hi there"},
	))
	if out != "P" {
		t.Errorf("public @hi routed to %q, want P", out)
	}
}

func TestRouterGroupScopeFiltering(t *testing.T) {
	b := &bot.Bot{
		Name:     "scoped",
		Triggers: []string{"@ask"},
		Groups:   []string{"group-A"},
	}
	h := &recordingHandler{replyText: "ok"}
	rtr, err := New([]Route{routeFromInline(t, b, h)}, WithLogger(nullLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// In-scope group.
	out := drain(rtr.Dispatch(context.Background(),
		chat.Envelope{ConvID: "group:group-A", Kind: "group", GroupID: "group-A", SenderPhone: "+15550000000"},
		chat.Message{Text: "hey @ask"},
	))
	if out != "ok" {
		t.Errorf("in-scope group got %q, want ok", out)
	}

	// Out-of-scope group.
	out = drain(rtr.Dispatch(context.Background(),
		chat.Envelope{ConvID: "group:group-B", Kind: "group", GroupID: "group-B", SenderPhone: "+15550000000"},
		chat.Message{Text: "hey @ask"},
	))
	if out != "" {
		t.Errorf("out-of-scope group should be silent, got %q", out)
	}
}

func TestRouterBufferDrainOnTrigger(t *testing.T) {
	memoryFull := bot.MemoryFull
	b := &bot.Bot{
		Name:        "only",
		Triggers:    []string{"@bot"},
		Permissions: bot.Permissions{Memory: memoryFull},
	}
	h := &recordingHandler{replyText: "ok"}
	rtr, err := New([]Route{routeFromInline(t, b, h)}, WithLogger(nullLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	env := chat.Envelope{ConvID: "peer", Kind: "dm", SenderPhone: "+15550000000"}

	// Two untriggered messages should buffer (no handler call).
	if out := drain(rtr.Dispatch(context.Background(), env, chat.Message{Text: "first"})); out != "" {
		t.Errorf("untriggered drain: got %q, want empty", out)
	}
	if out := drain(rtr.Dispatch(context.Background(), env, chat.Message{Text: "second"})); out != "" {
		t.Errorf("untriggered drain: got %q, want empty", out)
	}
	if _, _, calls := h.snapshot(); calls != 0 {
		t.Errorf("untriggered: handler invoked %d times, want 0", calls)
	}

	// Triggered message should flush the buffer and invoke the handler once
	// with <previous_messages> wrapping the two priors.
	if out := drain(rtr.Dispatch(context.Background(), env, chat.Message{Text: "@bot now"})); out != "ok" {
		t.Errorf("triggered drain: got %q, want ok", out)
	}
	conv, msg, calls := h.snapshot()
	if conv != "peer" {
		t.Errorf("handler conv = %q, want peer", conv)
	}
	if calls != 1 {
		t.Errorf("handler calls = %d, want 1", calls)
	}
	if !strings.Contains(msg.Text, "<previous_messages>") ||
		!strings.Contains(msg.Text, "first") ||
		!strings.Contains(msg.Text, "second") ||
		!strings.HasSuffix(msg.Text, "@bot now") {
		t.Errorf("priors not flushed into turn: %q", msg.Text)
	}
}

func TestRouterAttachmentStripping(t *testing.T) {
	no := false
	b := &bot.Bot{
		Name:        "strict",
		Triggers:    []string{"@x"},
		Permissions: bot.Permissions{Attachments: &no},
	}
	h := &recordingHandler{replyText: "ok"}
	rtr, err := New([]Route{routeFromInline(t, b, h)}, WithLogger(nullLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	env := chat.Envelope{ConvID: "c", Kind: "dm", SenderPhone: "+15550000000"}
	_ = drain(rtr.Dispatch(context.Background(), env, chat.Message{
		Text:        "@x here",
		Attachments: []chat.Attachment{{Path: "/tmp/file", Filename: "file"}},
	}))

	_, msg, _ := h.snapshot()
	if len(msg.Attachments) != 0 {
		t.Errorf("attachments not stripped: %+v", msg.Attachments)
	}
}

func TestRouterAttachmentsForwardedWhenAllowed(t *testing.T) {
	yes := true
	b := &bot.Bot{
		Name:        "lenient",
		Triggers:    []string{"@x"},
		Permissions: bot.Permissions{Attachments: &yes},
	}
	h := &recordingHandler{replyText: "ok"}
	rtr, err := New([]Route{routeFromInline(t, b, h)}, WithLogger(nullLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	env := chat.Envelope{ConvID: "c", Kind: "dm", SenderPhone: "+15550000000"}
	_ = drain(rtr.Dispatch(context.Background(), env, chat.Message{
		Text:        "@x here",
		Attachments: []chat.Attachment{{Path: "/tmp/file", Filename: "file"}},
	}))

	_, msg, _ := h.snapshot()
	if len(msg.Attachments) != 1 {
		t.Errorf("attachments should pass through: %+v", msg.Attachments)
	}
}

func TestRouterGroupMessageSilentOnNoTrigger(t *testing.T) {
	b := &bot.Bot{Name: "only", Triggers: []string{"@ask"}}
	h := &recordingHandler{replyText: "ok"}
	rtr, err := New([]Route{routeFromInline(t, b, h)}, WithLogger(nullLogger()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	out := drain(rtr.Dispatch(context.Background(),
		chat.Envelope{ConvID: "group:g", Kind: "group", GroupID: "g", SenderPhone: "+15550000000"},
		chat.Message{Text: "just chatting"},
	))
	if out != "" {
		t.Errorf("untriggered group should be silent, got %q", out)
	}
	if _, _, calls := h.snapshot(); calls != 0 {
		t.Errorf("handler should not run for untriggered group; calls = %d", calls)
	}
}
