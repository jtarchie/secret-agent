package bot

import (
	"strings"
	"testing"
)

func TestLoadToolHookShorthand(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo ok
    hooks:
      - on: before
        expr: "nil"
      - on: after
        js: "null"
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.Tools[0].Hooks) != 2 {
		t.Fatalf("hooks = %d", len(b.Tools[0].Hooks))
	}
	if got := b.Tools[0].Hooks[0].On; got != HookBeforeTool {
		t.Errorf("hook[0].on = %q, want before_tool", got)
	}
	if got := b.Tools[0].Hooks[1].On; got != HookAfterTool {
		t.Errorf("hook[1].on = %q, want after_tool", got)
	}
}

func TestLoadToolHookRejectsFilter(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo ok
    hooks:
      - on: before
        tool: other
        expr: "nil"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "tool:") {
		t.Errorf("error should mention tool filter: %v", err)
	}
}

func TestLoadToolHookRejectsModelEvent(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo ok
    hooks:
      - on: before_model
        expr: "nil"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadBotHookFilter(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: greet
    sh: echo ok
hooks:
  - on: before_tool
    tool: greet
    expr: "nil"
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := b.Hooks[0].Tool; got != "greet" {
		t.Errorf("hook tool = %q", got)
	}
}

func TestLoadBotHookFilterRejectsUnknownTool(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: greet
    sh: echo ok
hooks:
  - on: before_tool
    tool: missing
    expr: "nil"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name the missing tool: %v", err)
	}
}

func TestLoadBotHookFilterRejectsOnNonToolEvent(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
hooks:
  - on: before_model
    tool: whatever
    expr: "nil"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadBotHookRejectsUnknownEvent(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
hooks:
  - on: on_event
    expr: "nil"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadHookRejectsMultipleRuntimes(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
hooks:
  - on: before_model
    expr: "nil"
    js: "null"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "only one") {
		t.Errorf("error should mention single runtime: %v", err)
	}
}

func TestLoadHookRejectsNoRuntime(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
hooks:
  - on: before_model
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadAllBotEvents(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo ok
hooks:
  - on: before_tool
    expr: "nil"
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
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.Hooks) != 6 {
		t.Fatalf("hooks = %d, want 6", len(b.Hooks))
	}
}
