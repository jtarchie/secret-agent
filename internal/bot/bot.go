// Package bot loads YAML bot definitions.
package bot

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Bot struct {
	Name   string `yaml:"name"`
	System string `yaml:"system"`
	Tools  []Tool `yaml:"tools"`
}

type Tool struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Sh          string `yaml:"sh"`
}

func Load(path string) (*Bot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var b Bot
	if err := yaml.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	b.Name = strings.TrimSpace(b.Name)
	b.System = strings.TrimSpace(b.System)

	if b.Name == "" {
		return nil, fmt.Errorf("%s: name is required", path)
	}
	if b.System == "" {
		return nil, fmt.Errorf("%s: system is required", path)
	}

	for i := range b.Tools {
		t := &b.Tools[i]
		t.Name = strings.TrimSpace(t.Name)
		t.Description = strings.TrimSpace(t.Description)
		t.Sh = strings.TrimSpace(t.Sh)
		if t.Name == "" {
			return nil, fmt.Errorf("%s: tools[%d].name is required", path, i)
		}
		if t.Sh == "" {
			return nil, fmt.Errorf("%s: tool %q: sh is required", path, t.Name)
		}
	}

	return &b, nil
}
