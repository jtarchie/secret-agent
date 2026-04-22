package tool

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// fakeSender records every Send call so tests can assert on what was
// dispatched.
type fakeSender struct {
	mu    sync.Mutex
	calls []struct{ to, text string }
	err   error
}

func (f *fakeSender) Send(_ context.Context, to, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ to, text string }{to, text})
	return f.err
}

func (f *fakeSender) snapshot() []struct{ to, text string } {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]struct{ to, text string }(nil), f.calls...)
}

func TestSaSendBuiltinDispatches(t *testing.T) {
	sender := &fakeSender{}
	reg := chat.SenderRegistry{"signal": sender}

	_, err := RunShellScript(
		context.Background(),
		`sa_send signal +15551234567 "hello there"`,
		"test",
		reg,
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].to != "+15551234567" {
		t.Errorf("to = %q", calls[0].to)
	}
	if calls[0].text != "hello there" {
		t.Errorf("text = %q", calls[0].text)
	}
}

func TestSaSendBuiltinUnknownTransport(t *testing.T) {
	reg := chat.SenderRegistry{"signal": &fakeSender{}}
	_, err := RunShellScript(
		context.Background(),
		`sa_send slack U12345 "hi"`,
		"test",
		reg,
	)
	if err == nil {
		t.Fatal("expected error for unknown transport")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("error should name missing transport: %v", err)
	}
}

func TestSaSendBuiltinWrongArity(t *testing.T) {
	reg := chat.SenderRegistry{"signal": &fakeSender{}}
	_, err := RunShellScript(
		context.Background(),
		`sa_send signal +15551234567`,
		"test",
		reg,
	)
	if err == nil {
		t.Fatal("expected error for missing arg")
	}
}

func TestSaSendBuiltinUnavailableWithoutRegistry(t *testing.T) {
	// Without a registry, sa_send falls through to the default exec
	// handler, which treats it as an unknown command.
	_, err := RunShellScript(
		context.Background(),
		`sa_send signal +15551234567 "hi"`,
		"test",
		nil,
	)
	if err == nil {
		t.Fatal("expected error when registry is nil")
	}
}

func TestDispatchSendRejectsEmpty(t *testing.T) {
	reg := chat.SenderRegistry{"signal": &fakeSender{}}
	err := DispatchSend(context.Background(), reg, "", "+1", "hi")
	if err == nil {
		t.Fatal("expected error for empty transport")
	}
	err = DispatchSend(context.Background(), reg, "signal", "", "hi")
	if err == nil {
		t.Fatal("expected error for empty to")
	}
}

func TestSendMessageToolDispatches(t *testing.T) {
	sender := &fakeSender{}
	reg := chat.SenderRegistry{"signal": sender}
	tool, err := NewSendMessageTool(reg)
	if err != nil {
		t.Fatalf("NewSendMessageTool: %v", err)
	}
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.Name() != SendMessageToolName {
		t.Errorf("name = %q", tool.Name())
	}
}

func TestSendMessageToolEmptyRegistry(t *testing.T) {
	_, err := NewSendMessageTool(nil)
	if err == nil {
		t.Fatal("expected error for empty registry")
	}
}

func TestRunExprSendMessageBinding(t *testing.T) {
	sender := &fakeSender{}
	reg := chat.SenderRegistry{"signal": sender}
	_, err := RunExpr(
		context.Background(),
		`send_message("signal", "+15551234567", "from expr")`,
		"test",
		reg,
	)
	if err != nil {
		t.Fatalf("RunExpr: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 || calls[0].text != "from expr" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestRunJsSendMessageBinding(t *testing.T) {
	sender := &fakeSender{}
	reg := chat.SenderRegistry{"signal": sender}
	_, err := RunJs(
		context.Background(),
		`send_message("signal", "+15551234567", "from js")`,
		"test",
		reg,
	)
	if err != nil {
		t.Fatalf("RunJs: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 || calls[0].text != "from js" {
		t.Fatalf("calls = %+v", calls)
	}
}
