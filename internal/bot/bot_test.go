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

func TestLoadPermissionsDefaults(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !b.Permissions.AttachmentsAllowed() {
		t.Error("attachments default should be true")
	}
	if got := b.Permissions.MemoryOrDefault(); got != MemoryFull {
		t.Errorf("memory default = %q, want %q", got, MemoryFull)
	}
}

func TestLoadPermissionsExplicit(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
permissions:
  attachments: false
  memory: none
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if b.Permissions.AttachmentsAllowed() {
		t.Error("attachments should be false when explicitly set")
	}
	if got := b.Permissions.MemoryOrDefault(); got != MemoryNone {
		t.Errorf("memory = %q, want %q", got, MemoryNone)
	}
}

func TestLoadMCPStdio(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
mcp:
  - name: fs
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
    env:
      FOO: bar
    tool_filter: [read_file, list_directory]
    require_confirmation: true
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.MCP) != 1 {
		t.Fatalf("len(mcp) = %d, want 1", len(b.MCP))
	}
	m := b.MCP[0]
	if m.Name != "fs" {
		t.Errorf("name = %q", m.Name)
	}
	if m.Command != "npx" {
		t.Errorf("command = %q", m.Command)
	}
	if len(m.Args) != 3 || m.Args[2] != "/data" {
		t.Errorf("args = %v", m.Args)
	}
	if m.Env["FOO"] != "bar" {
		t.Errorf("env[FOO] = %q", m.Env["FOO"])
	}
	if !m.RequireConfirmation {
		t.Error("require_confirmation should be true")
	}
	if len(m.ToolFilter) != 2 {
		t.Errorf("tool_filter = %v", m.ToolFilter)
	}
}

func TestLoadMCPHTTP(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
mcp:
  - name: maps
    url: https://example.com/mcp
    headers:
      Authorization: Bearer token
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m := b.MCP[0]
	if m.URL != "https://example.com/mcp" {
		t.Errorf("url = %q", m.URL)
	}
	if m.Headers["Authorization"] != "Bearer token" {
		t.Errorf("headers = %v", m.Headers)
	}
}

func TestLoadMCPRejectsBothTransports(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
mcp:
  - name: both
    command: foo
    url: https://example.com/mcp
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error when both command and url set")
	}
	if !strings.Contains(err.Error(), "only one") {
		t.Errorf("error should say only one: %v", err)
	}
}

func TestLoadMCPRejectsNoTransport(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
mcp:
  - name: empty
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error when neither command nor url set")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("error should mention exactly one: %v", err)
	}
}

func TestLoadMCPRejectsToolCollision(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
tools:
  - name: shared
    sh: echo hi
mcp:
  - name: shared
    command: foo
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("error should mention conflict: %v", err)
	}
}

func TestLoadMCPRejectsInvalidName(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
mcp:
  - name: "bad-name"
    command: foo
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected name validation error")
	}
}

func TestLoadUsersValid(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
users:
  - "+15551234567"
  - "  +15557654321  "
  - "+15551234567"
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.Users) != 2 {
		t.Fatalf("len(users) = %d, want 2 (trimmed + deduped); got %v", len(b.Users), b.Users)
	}
	if b.Users[0] != "+15551234567" || b.Users[1] != "+15557654321" {
		t.Errorf("users = %v", b.Users)
	}
}

func TestLoadUsersRejectsNonE164(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
users:
  - "not a phone"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for non-E.164 user")
	}
	if !strings.Contains(err.Error(), "E.164") {
		t.Errorf("error should mention E.164: %v", err)
	}
}

func TestLoadGroupsTrimAndDedupe(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
groups:
  - "  group-A  "
  - "group-B"
  - "group-A"
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.Groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2; got %v", len(b.Groups), b.Groups)
	}
	if b.Groups[0] != "group-A" || b.Groups[1] != "group-B" {
		t.Errorf("groups = %v", b.Groups)
	}
}

func TestLoadSlackUsersValid(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
slack_users:
  - "U12345"
  - "  W6789ABC  "
  - "U12345"
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.SlackUsers) != 2 {
		t.Fatalf("len(slack_users) = %d, want 2 (trimmed + deduped); got %v", len(b.SlackUsers), b.SlackUsers)
	}
	if b.SlackUsers[0] != "U12345" || b.SlackUsers[1] != "W6789ABC" {
		t.Errorf("slack_users = %v", b.SlackUsers)
	}
}

func TestLoadSlackUsersRejectsBadID(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
slack_users:
  - "not-a-slack-id"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for non-Slack user ID")
	}
	if !strings.Contains(err.Error(), "Slack user ID") {
		t.Errorf("error should mention Slack user ID: %v", err)
	}
}

func TestLoadSlackChannelsValid(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
slack_channels:
  - "C12345"
  - "  D67890A  "
  - "GABCDEF"
  - "C12345"
`)
	b, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(b.SlackChannels) != 3 {
		t.Fatalf("len(slack_channels) = %d, want 3; got %v", len(b.SlackChannels), b.SlackChannels)
	}
	if b.SlackChannels[0] != "C12345" || b.SlackChannels[1] != "D67890A" || b.SlackChannels[2] != "GABCDEF" {
		t.Errorf("slack_channels = %v", b.SlackChannels)
	}
}

func TestLoadSlackChannelsRejectsBadID(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
slack_channels:
  - "bad"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for non-Slack channel ID")
	}
	if !strings.Contains(err.Error(), "Slack channel ID") {
		t.Errorf("error should mention Slack channel ID: %v", err)
	}
}

func TestLoadPermissionsInvalidMemory(t *testing.T) {
	p := writeBot(t, `
name: b
system: s
permissions:
  memory: wiggle
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for unknown memory mode")
	}
}
