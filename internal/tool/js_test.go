package tool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"

	"github.com/jtarchie/secret-agent/internal/bot"
)

func TestNewJsCompileError(t *testing.T) {
	_, err := NewJs("bad", "d", "function (", map[string]bot.Param{})
	if err == nil {
		t.Fatal("expected compile error for invalid JS")
	}
}

func TestNewJsValid(t *testing.T) {
	tool, err := NewJs("shout", "uppercases", `who.toUpperCase() + "!"`, map[string]bot.Param{
		"who": {Type: bot.ParamString, Required: true},
	})
	if err != nil {
		t.Fatalf("NewJs: %v", err)
	}
	if tool == nil {
		t.Fatal("expected tool")
	}
}

// TestGojaInterruptOnContextCancel verifies the cancel-wiring pattern used
// inside NewJs: a background goroutine watching ctx.Done() calls Interrupt
// on the VM, which turns a runaway loop into an error within ms.
func TestGojaInterruptOnContextCancel(t *testing.T) {
	vm := goja.New()
	program, err := goja.Compile("loop", "while(true){}", true)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-done:
		}
	}()

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err = vm.RunProgram(program)
	if err == nil {
		t.Fatal("expected interrupt error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "interrupt") && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("expected interrupt/cancel error, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("interrupt too slow: %v", elapsed)
	}
}
