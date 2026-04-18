package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBot(t *testing.T, yaml string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bot.yml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestLoadAttachmentShorthand(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo ok
    params:
      file: attachment!
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := b.Tools[0].Params["file"]
	if got.Type != ParamAttachment {
		t.Errorf("type = %q", got.Type)
	}
	if !got.Required {
		t.Error("expected required")
	}
}

func TestLoadAttachmentMapping(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo ok
    params:
      file:
        type: attachment
        description: the file
        required: true
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := b.Tools[0].Params["file"]
	if got.Type != ParamAttachment {
		t.Errorf("type = %q", got.Type)
	}
	if got.Description != "the file" {
		t.Errorf("desc = %q", got.Description)
	}
}

func TestLoadAttachmentRejectsDefaultShorthand(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo ok
    params:
      file: attachment=foo
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("error should mention default: %v", err)
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestLoadAgents(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "child.yml", `
name: child
system: You are a greeter.
`)
	parent := writeFile(t, dir, "parent.yml", `
name: parent
system: You coordinate.
agents:
  greeter:
    file: ./child.yml
    description: Greets the user.
    skip_summarization: true
`)

	b, err := Load(parent)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ref, ok := b.Agents["greeter"]
	if !ok {
		t.Fatal("missing greeter")
	}
	if ref.Description != "Greets the user." {
		t.Errorf("desc = %q", ref.Description)
	}
	if !ref.SkipSummarization {
		t.Error("skip_summarization should be true")
	}
	if ref.Bot == nil || ref.Bot.Name != "child" {
		t.Errorf("child not resolved: %+v", ref.Bot)
	}
}

func TestLoadAgentsMissingFile(t *testing.T) {
	dir := t.TempDir()
	parent := writeFile(t, dir, "parent.yml", `
name: parent
system: s
agents:
  missing:
    file: ./nope.yml
`)
	_, err := Load(parent)
	if err == nil {
		t.Fatal("expected error for missing child")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name the agent: %v", err)
	}
}

func TestLoadToolExprRuntime(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: add
    expr: a + b
    params:
      a: number!
      b: number!
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if b.Tools[0].Expr != "a + b" {
		t.Errorf("expr = %q", b.Tools[0].Expr)
	}
}

func TestLoadToolJsRuntime(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: shout
    js: who.toUpperCase()
    params:
      who: string!
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if b.Tools[0].Js != "who.toUpperCase()" {
		t.Errorf("js = %q", b.Tools[0].Js)
	}
}

func TestLoadToolRejectsNoRuntime(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    params:
      x: string!
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for tool with no runtime")
	}
	if !strings.Contains(err.Error(), "sh, expr, js") {
		t.Errorf("error should list runtimes: %v", err)
	}
}

func TestLoadToolRejectsMultipleRuntimes(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo hi
    expr: "42"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for tool with multiple runtimes")
	}
	if !strings.Contains(err.Error(), "only one") {
		t.Errorf("error should say only one: %v", err)
	}
}

func TestLoadAgentsCycle(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yml")
	b := filepath.Join(dir, "b.yml")
	if err := os.WriteFile(a, []byte(`
name: a
system: s
agents:
  bb:
    file: ./b.yml
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(`
name: b
system: s
agents:
  aa:
    file: ./a.yml
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(a)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle: %v", err)
	}
}

func TestLoadAgentsCollidesWithTool(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "child.yml", `
name: child
system: s
`)
	parent := writeFile(t, dir, "parent.yml", `
name: parent
system: s
tools:
  - name: greeter
    sh: echo hi
agents:
  greeter:
    file: ./child.yml
`)
	_, err := Load(parent)
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("error should mention conflict: %v", err)
	}
}

func TestLoadAgentsInvalidName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "child.yml", `
name: child
system: s
`)
	parent := writeFile(t, dir, "parent.yml", `
name: parent
system: s
agents:
  "bad-name":
    file: ./child.yml
`)
	_, err := Load(parent)
	if err == nil {
		t.Fatal("expected name validation error")
	}
}

func TestLoadAttachmentRejectsDefaultMapping(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: t
    sh: echo ok
    params:
      file:
        type: attachment
        default: "foo"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadTriggersTrimAndDedupe(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
triggers:
  - "  @bot  "
  - "@BOT"
  - "@willow"
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.Triggers) != 2 {
		t.Fatalf("len(triggers) = %d, want 2 (trimmed + deduped case-insensitively); got %v", len(b.Triggers), b.Triggers)
	}
	if b.Triggers[0] != "@bot" || b.Triggers[1] != "@willow" {
		t.Errorf("triggers = %v, want [@bot @willow]", b.Triggers)
	}
}

func TestLoadTriggersRejectEmpty(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
triggers:
  - "@bot"
  - ""
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for empty trigger")
	}
}
