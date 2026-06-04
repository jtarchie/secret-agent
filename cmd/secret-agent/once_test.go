package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/runtime"
)

// pipeWith returns the read end of an os.Pipe preloaded with content, so a
// helper that inspects *os.File sees it as piped (not a TTY).
func pipeWith(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	go func() {
		_, _ = io.WriteString(w, content)
		_ = w.Close()
	}()
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestResolveMessageFlagWins(t *testing.T) {
	msg, err := resolveMessage("from flag", nil, pipeWith(t, "from stdin\n"))
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if msg.Text != "from flag" {
		t.Errorf("Text = %q, want %q", msg.Text, "from flag")
	}
}

func TestResolveMessageReadsStdin(t *testing.T) {
	msg, err := resolveMessage("", nil, pipeWith(t, "piped message\n\n"))
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if msg.Text != "piped message" { // trailing newlines trimmed
		t.Errorf("Text = %q, want %q", msg.Text, "piped message")
	}
}

func TestResolveMessageAttachmentsOnly(t *testing.T) {
	msg, err := resolveMessage("", []string{"/tmp/a.png"}, pipeWith(t, ""))
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if msg.Text != "" {
		t.Errorf("Text = %q, want empty", msg.Text)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("Attachments = %d, want 1", len(msg.Attachments))
	}
}

func TestResolveMessageEmptyErrors(t *testing.T) {
	_, err := resolveMessage("", nil, pipeWith(t, ""))
	if err == nil {
		t.Fatal("want error for empty message, got nil")
	}
}

func TestBuildAttachments(t *testing.T) {
	if got := buildAttachments(nil); got != nil {
		t.Errorf("buildAttachments(nil) = %v, want nil", got)
	}
	got := buildAttachments([]string{"/x/y/report.pdf", "notes.txt"})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Filename != "report.pdf" || got[0].Path != "/x/y/report.pdf" || got[0].ContentType != "" {
		t.Errorf("got[0] = %+v, want base filename, full path, empty content type", got[0])
	}
	if got[1].Filename != "notes.txt" {
		t.Errorf("got[1].Filename = %q, want notes.txt", got[1].Filename)
	}
}

func TestResolveDefaultLLM(t *testing.T) {
	llm, provider, name, err := resolveDefaultLLM("", "", "")
	if err != nil {
		t.Fatalf("resolveDefaultLLM(empty): %v", err)
	}
	if llm != nil || provider != "" || name != "" {
		t.Errorf("empty model = (%v,%q,%q), want (nil,\"\",\"\")", llm, provider, name)
	}

	llm, provider, name, err = resolveDefaultLLM("openai/gpt-4o", "key", "http://127.0.0.1:1234/v1")
	if err != nil {
		t.Fatalf("resolveDefaultLLM(openai): %v", err)
	}
	if llm == nil {
		t.Error("llm = nil, want non-nil")
	}
	if provider != "openai" || name != "gpt-4o" {
		t.Errorf("(provider,name) = (%q,%q), want (openai,gpt-4o)", provider, name)
	}

	_, _, _, err = resolveDefaultLLM("bogus/x", "", "")
	if err == nil {
		t.Error("want error for unknown provider without base-url, got nil")
	}
}

func TestBuildRuntimeNoModel(t *testing.T) {
	b, err := bot.Load("../../examples/hello-world.yml")
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	c := &OnceCmd{SkipPreflight: true} // no --model, YAML declares none
	_, err = c.buildRuntime(context.Background(), b)
	if !errors.Is(err, errNoModel) {
		t.Errorf("buildRuntime err = %v, want errNoModel", err)
	}
}

func TestMarshalOnceJSON(t *testing.T) {
	calls := []runtime.ToolCall{
		{Name: "greet", Args: map[string]any{"who": "Ada"}, Result: map[string]any{"output": "Hello, Ada!"}},
	}

	usage := &runtime.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}

	var ok onceOutput
	err := json.Unmarshal(marshalOnceJSON("hi", calls, usage, nil), &ok)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok.Output != "hi" {
		t.Errorf("Output = %q, want hi", ok.Output)
	}
	if ok.Error != nil {
		t.Errorf("Error = %v, want nil", *ok.Error)
	}
	if len(ok.ToolCalls) != 1 || ok.ToolCalls[0].Name != "greet" {
		t.Errorf("ToolCalls = %+v, want one greet call", ok.ToolCalls)
	}
	if ok.Usage == nil {
		t.Fatal("Usage = nil, want populated")
	}
	if ok.Usage.InputTokens != 10 || ok.Usage.OutputTokens != 5 || ok.Usage.TotalTokens != 15 {
		t.Errorf("Usage = %+v, want {10 5 15}", *ok.Usage)
	}

	var failed onceOutput
	err = json.Unmarshal(marshalOnceJSON("", nil, nil, errors.New("boom")), &failed)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if failed.Error == nil || *failed.Error != "boom" {
		t.Errorf("Error = %v, want \"boom\"", failed.Error)
	}
	if failed.Usage != nil {
		t.Errorf("Usage = %+v, want nil when no usage reported", *failed.Usage)
	}
}
