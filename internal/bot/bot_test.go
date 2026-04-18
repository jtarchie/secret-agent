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
