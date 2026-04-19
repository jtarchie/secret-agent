package runtime

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"testing"

	adkmodel "google.golang.org/adk/model"

	"github.com/jtarchie/secret-agent/internal/bot"
)

// stubLLM is a zero-behavior ADK model sufficient to satisfy buildAgent's
// signature. It never receives a request in these tests; we only exercise
// the construction path where hooks are compiled and wired into the
// llmagent config.
type stubLLM struct{}

func (stubLLM) Name() string { return "stub" }
func (stubLLM) GenerateContent(_ context.Context, _ *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {}
}

func writeBot(t *testing.T, body string) *bot.Bot {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bot.yml")
	err := os.WriteFile(p, []byte(body), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := bot.Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return b
}

func TestBuildAgentWithAllHookEvents(t *testing.T) {
	b := writeBot(t, `
name: b
system: s
tools:
  - name: greet
    sh: echo hi
    hooks:
      - on: before
        expr: "nil"
      - on: after
        js: "null"
hooks:
  - on: before_tool
    tool: greet
    sh: ":"
  - on: after_tool
    expr: "nil"
  - on: before_model
    expr: "nil"
  - on: after_model
    expr: "nil"
  - on: before_agent
    expr: "nil"
  - on: after_agent
    expr: "nil"
`)
	_, err := (&builder{}).buildAgent(b.Name, "test", b, stubLLM{}, true)
	if err != nil {
		t.Fatalf("buildAgent: %v", err)
	}
}

func TestBuildAgentNoHooksUnchanged(t *testing.T) {
	b := writeBot(t, `
name: b
system: s
tools:
  - name: greet
    sh: echo hi
`)
	_, err := (&builder{}).buildAgent(b.Name, "test", b, stubLLM{}, true)
	if err != nil {
		t.Fatalf("buildAgent: %v", err)
	}
}

func TestBuildAgentRejectsBadHookScript(t *testing.T) {
	// Compilation error inside the hook script should surface from
	// buildAgent, not later at runtime.
	p := filepath.Join(t.TempDir(), "bot.yml")
	err := os.WriteFile(p, []byte(`
name: b
system: s
hooks:
  - on: before_model
    expr: "((("
`), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := bot.Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	_, err = (&builder{}).buildAgent(b.Name, "test", b, stubLLM{}, true)
	if err == nil {
		t.Fatal("expected buildAgent to fail on bad hook expr")
	}
}
