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
	want := map[string]bool{
		"code-reviewer": true,
		"pii":           true,
		"summarizer":    true,
		"translator":    true,
	}
	if len(items) != len(want) {
		t.Fatalf("got %d builtins, want %d (%v)", len(items), len(want), items)
	}
	seen := make(map[string]bool, len(items))
	var prev string
	for i, it := range items {
		if !want[it.Name] {
			t.Errorf("items[%d].Name = %q is not an expected builtin", i, it.Name)
		}
		if seen[it.Name] {
			t.Errorf("duplicate builtin %q", it.Name)
		}
		seen[it.Name] = true
		if it.Description == "" {
			t.Errorf("builtin %q has empty description", it.Name)
		}
		if i > 0 && it.Name < prev {
			t.Errorf("builtins not sorted: %q came after %q", it.Name, prev)
		}
		prev = it.Name
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("expected builtin %q not present", name)
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
