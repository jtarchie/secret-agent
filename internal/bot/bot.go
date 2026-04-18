// Package bot loads YAML bot definitions.
package bot

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Bot struct {
	Name   string `yaml:"name"`
	System string `yaml:"system"`
	Tools  []Tool `yaml:"tools"`
}

type Tool struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Sh          string           `yaml:"sh"`
	Params      map[string]Param `yaml:"params"`
}

type ParamType string

const (
	ParamString     ParamType = "string"
	ParamInteger    ParamType = "integer"
	ParamNumber     ParamType = "number"
	ParamBoolean    ParamType = "boolean"
	ParamAttachment ParamType = "attachment"
)

type Param struct {
	Type        ParamType `yaml:"type"`
	Description string    `yaml:"description"`
	Required    bool      `yaml:"required"`
	Default     any       `yaml:"default"`
	Enum        []any     `yaml:"enum"`
}

var (
	shorthandRe    = regexp.MustCompile(`^(string|integer|number|boolean|attachment)(!)?(?:=(.*))?$`)
	paramNameRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	reservedParams = map[string]struct{}{
		"path": {}, "home": {}, "user": {}, "shell": {}, "pwd": {},
		"ifs": {}, "ld_preload": {}, "ld_library_path": {},
	}
)

// UnmarshalYAML accepts either a shorthand scalar ("string!", "integer=1")
// or a full mapping.
func (p *Param) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return p.parseShorthand(node.Value)
	case yaml.MappingNode:
		type raw Param
		var r raw
		if err := node.Decode(&r); err != nil {
			return err
		}
		*p = Param(r)
		return nil
	default:
		return fmt.Errorf("param must be a scalar shorthand or a mapping, got %v", node.Kind)
	}
}

func (p *Param) parseShorthand(s string) error {
	m := shorthandRe.FindStringSubmatch(s)
	if m == nil {
		return fmt.Errorf("invalid shorthand %q: expected <type>[!][=<default>] where type is string|integer|number|boolean", s)
	}
	p.Type = ParamType(m[1])
	p.Required = m[2] == "!"
	if m[3] != "" {
		if p.Required {
			return fmt.Errorf("invalid shorthand %q: ! (required) and = (default) are mutually exclusive", s)
		}
		v, err := coerceScalar(m[3], p.Type)
		if err != nil {
			return fmt.Errorf("invalid default in shorthand %q: %w", s, err)
		}
		p.Default = v
	}
	return nil
}

func coerceScalar(s string, t ParamType) (any, error) {
	switch t {
	case ParamString:
		return s, nil
	case ParamInteger:
		return strconv.ParseInt(s, 10, 64)
	case ParamNumber:
		return strconv.ParseFloat(s, 64)
	case ParamBoolean:
		return strconv.ParseBool(s)
	case ParamAttachment:
		return nil, fmt.Errorf("attachment params cannot have a default")
	}
	return nil, fmt.Errorf("unknown type %q", t)
}

func (p *Param) validate(toolName, paramName string) error {
	switch p.Type {
	case ParamString, ParamInteger, ParamNumber, ParamBoolean, ParamAttachment:
	default:
		return fmt.Errorf("tool %q param %q: unknown type %q (want string|integer|number|boolean|attachment)", toolName, paramName, p.Type)
	}

	if !paramNameRe.MatchString(paramName) {
		return fmt.Errorf("tool %q param %q: name must match [A-Za-z_][A-Za-z0-9_]*", toolName, paramName)
	}
	if _, reserved := reservedParams[strings.ToLower(paramName)]; reserved {
		return fmt.Errorf("tool %q param %q: reserved env var name", toolName, paramName)
	}

	if p.Required && p.Default != nil {
		return fmt.Errorf("tool %q param %q: required and default are mutually exclusive", toolName, paramName)
	}

	if p.Type == ParamAttachment && p.Default != nil {
		return fmt.Errorf("tool %q param %q: attachment type cannot have a default", toolName, paramName)
	}

	if len(p.Enum) > 0 {
		if p.Type != ParamString {
			return fmt.Errorf("tool %q param %q: enum is only supported for string type", toolName, paramName)
		}
		if p.Default != nil {
			found := false
			for _, e := range p.Enum {
				if e == p.Default {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("tool %q param %q: default %v not in enum", toolName, paramName, p.Default)
			}
		}
	}

	return nil
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
		for name, p := range t.Params {
			if err := p.validate(t.Name, name); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
	}

	return &b, nil
}
