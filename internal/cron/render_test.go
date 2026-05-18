package cron

import (
	"strings"
	"testing"

	"github.com/jtarchie/secret-agent/internal/bot"
)

func TestTruncateOutputUnderCap(t *testing.T) {
	s := strings.Repeat("a", 100)
	got := truncateOutput(s)
	if got != s {
		t.Errorf("input under cap should pass through unchanged")
	}
}

func TestTruncateOutputAtCap(t *testing.T) {
	s := strings.Repeat("a", MaxScriptOutputBytes)
	got := truncateOutput(s)
	if got != s {
		t.Errorf("input exactly at cap should pass through unchanged")
	}
}

func TestTruncateOutputOverCap(t *testing.T) {
	const extra = 500
	s := strings.Repeat("a", MaxScriptOutputBytes+extra)
	got := truncateOutput(s)
	if !strings.HasSuffix(got, "[truncated 500 bytes]") {
		t.Errorf("missing truncation marker; tail = %q", got[len(got)-40:])
	}
	if !strings.HasPrefix(got, strings.Repeat("a", MaxScriptOutputBytes)) {
		t.Errorf("first MaxScriptOutputBytes should be preserved verbatim")
	}
}

func TestRenderPromptTemplateSuccess(t *testing.T) {
	entry := bot.Cron{Name: "n", Prompt: "out={{.Output}} err={{.Err}} code={{.ExitCode}}"}
	res := scriptResult{Output: "hello", Err: "boom", ExitCode: 1}
	got, err := renderPromptTemplate(entry, res)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := "out=hello err=boom code=1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderPromptTemplateParseError(t *testing.T) {
	entry := bot.Cron{Name: "n", Prompt: "hello {{ .Output"}
	_, err := renderPromptTemplate(entry, scriptResult{})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse prompt template") {
		t.Errorf("error = %v, want wrapped with 'parse prompt template'", err)
	}
}

func TestRenderPromptTemplateExecutionError(t *testing.T) {
	entry := bot.Cron{Name: "n", Prompt: "{{.NotAField.X}}"}
	_, err := renderPromptTemplate(entry, scriptResult{})
	if err == nil {
		t.Fatal("expected execution error")
	}
	if !strings.Contains(err.Error(), "render prompt template") {
		t.Errorf("error = %v, want wrapped with 'render prompt template'", err)
	}
}
