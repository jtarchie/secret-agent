package bot

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestListBuiltins(t *testing.T) {
	items, err := ListBuiltins()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"code-reviewer", "summarizer", "translator"}
	if len(items) != len(want) {
		t.Fatalf("got %d builtins, want %d (%v)", len(items), len(want), items)
	}
	for i, name := range want {
		if items[i].Name != name {
			t.Errorf("items[%d].Name = %q, want %q", i, items[i].Name, name)
		}
		if items[i].Description == "" {
			t.Errorf("items[%d] (%s) has empty description", i, items[i].Name)
		}
	}
}

func TestLookupBuiltin(t *testing.T) {
	data, info, ok := LookupBuiltin("summarizer")
	if !ok {
		t.Fatal("expected summarizer to exist")
	}
	if info.Name != "summarizer" {
		t.Errorf("info.Name = %q", info.Name)
	}
	var probe struct {
		Name   string `yaml:"name"`
		System string `yaml:"system"`
	}
	err := yaml.Unmarshal(data, &probe)
	if err != nil {
		t.Fatalf("unmarshal embedded yaml: %v", err)
	}
	if probe.Name != "summarizer" {
		t.Errorf("yaml name = %q", probe.Name)
	}
	if probe.System == "" {
		t.Error("embedded summarizer has empty system prompt")
	}
}

func TestLookupBuiltinUnknown(t *testing.T) {
	_, _, ok := LookupBuiltin("nope-not-here")
	if ok {
		t.Error("expected lookup of unknown builtin to fail")
	}
}
