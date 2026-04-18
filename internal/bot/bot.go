// Package bot loads YAML bot definitions.
package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Bot struct {
	Name     string              `yaml:"name"`
	System   string              `yaml:"system"`
	Triggers []string            `yaml:"triggers,omitempty"`
	Tools    []Tool              `yaml:"tools"`
	Agents   map[string]AgentRef `yaml:"agents"`
	Hooks    []Hook              `yaml:"hooks"`
}

// HookEvent names an ADK extension point a hook attaches to.
type HookEvent string

const (
	HookBeforeTool  HookEvent = "before_tool"
	HookAfterTool   HookEvent = "after_tool"
	HookBeforeModel HookEvent = "before_model"
	HookAfterModel  HookEvent = "after_model"
	HookBeforeAgent HookEvent = "before_agent"
	HookAfterAgent  HookEvent = "after_agent"
)

// Hook is a user-defined callback attached to an ADK extension point.
// Exactly one of Sh/Expr/Js is set (same discipline as Tool). Tool is an
// optional name filter valid only on bot-level tool hooks.
type Hook struct {
	On   HookEvent `yaml:"on"`
	Tool string    `yaml:"tool,omitempty"`
	Sh   string    `yaml:"sh,omitempty"`
	Expr string    `yaml:"expr,omitempty"`
	Js   string    `yaml:"js,omitempty"`
}

// AgentRef is a reference to a sub-agent defined in its own YAML file.
// The map key under `agents:` becomes the tool name the parent LLM sees,
// independent of the child's own `name:` field.
type AgentRef struct {
	File              string `yaml:"file"`
	Description       string `yaml:"description"`
	SkipSummarization bool   `yaml:"skip_summarization"`
	// Attachments, when true, exposes an optional `attachments` parameter to
	// the parent LLM so it can opt-in to forwarding current-turn attachments
	// to the sub-agent. Default false: the sub-agent never sees attachments.
	Attachments bool `yaml:"attachments"`

	// Bot is the resolved child bot, populated by Load. Not read from YAML.
	Bot *Bot `yaml:"-"`
}

const maxAgentDepth = 8

type Tool struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Sh          string           `yaml:"sh"`
	Expr        string           `yaml:"expr"`
	Js          string           `yaml:"js"`
	Params      map[string]Param `yaml:"params"`
	Hooks       []Hook           `yaml:"hooks"`
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

var allowedBotHookEvents = map[HookEvent]struct{}{
	HookBeforeTool:  {},
	HookAfterTool:   {},
	HookBeforeModel: {},
	HookAfterModel:  {},
	HookBeforeAgent: {},
	HookAfterAgent:  {},
}

// normalizeToolHook validates a tool-scoped hook and expands the shorthand
// "before"/"after" values of `on:` to the full event name. Tool-scoped
// hooks cannot set the `tool:` filter field.
func normalizeToolHook(h *Hook) error {
	h.Sh = strings.TrimSpace(h.Sh)
	h.Expr = strings.TrimSpace(h.Expr)
	h.Js = strings.TrimSpace(h.Js)

	if h.Tool != "" {
		return fmt.Errorf("`tool:` filter is only valid on bot-level hooks")
	}

	switch h.On {
	case "before", HookBeforeTool:
		h.On = HookBeforeTool
	case "after", HookAfterTool:
		h.On = HookAfterTool
	case "":
		return fmt.Errorf("`on:` is required (before|after)")
	default:
		return fmt.Errorf("`on: %s` is not valid on a tool-scoped hook (want before|after)", h.On)
	}

	return validateHookBody(h)
}

// normalizeBotHook validates a bot-level hook. The `tool:` filter is only
// valid on tool events and, when present, must name a declared tool.
func normalizeBotHook(h *Hook, toolNames map[string]struct{}) error {
	h.Sh = strings.TrimSpace(h.Sh)
	h.Expr = strings.TrimSpace(h.Expr)
	h.Js = strings.TrimSpace(h.Js)
	h.Tool = strings.TrimSpace(h.Tool)

	if h.On == "" {
		return fmt.Errorf("`on:` is required")
	}
	if _, ok := allowedBotHookEvents[h.On]; !ok {
		return fmt.Errorf("`on: %s` is not a valid event", h.On)
	}

	if h.Tool != "" {
		if h.On != HookBeforeTool && h.On != HookAfterTool {
			return fmt.Errorf("`tool:` filter is only valid on before_tool/after_tool (got %s)", h.On)
		}
		if _, ok := toolNames[h.Tool]; !ok {
			return fmt.Errorf("`tool: %s` does not name a declared tool", h.Tool)
		}
	}

	return validateHookBody(h)
}

func validateHookBody(h *Hook) error {
	set := []string{}
	if h.Sh != "" {
		set = append(set, "sh")
	}
	if h.Expr != "" {
		set = append(set, "expr")
	}
	if h.Js != "" {
		set = append(set, "js")
	}
	switch len(set) {
	case 0:
		return fmt.Errorf("exactly one of sh, expr, js is required")
	case 1:
		return nil
	default:
		return fmt.Errorf("only one of sh, expr, js may be set (got %s)", strings.Join(set, ", "))
	}
}

func Load(path string) (*Bot, error) {
	return loadBot(path, map[string]bool{}, 0)
}

func loadBot(path string, visited map[string]bool, depth int) (*Bot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	if visited[abs] {
		return nil, fmt.Errorf("agent cycle detected at %s", abs)
	}
	if depth > maxAgentDepth {
		return nil, fmt.Errorf("agent nesting depth exceeded at %s (max %d)", abs, maxAgentDepth)
	}
	visited[abs] = true
	defer delete(visited, abs)

	data, err := os.ReadFile(abs)
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

	if len(b.Triggers) > 0 {
		seen := make(map[string]struct{}, len(b.Triggers))
		deduped := b.Triggers[:0]
		for i, t := range b.Triggers {
			t = strings.TrimSpace(t)
			if t == "" {
				return nil, fmt.Errorf("%s: triggers[%d]: must not be empty", path, i)
			}
			key := strings.ToLower(t)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			deduped = append(deduped, t)
		}
		b.Triggers = deduped
	}

	for i := range b.Tools {
		t := &b.Tools[i]
		t.Name = strings.TrimSpace(t.Name)
		t.Description = strings.TrimSpace(t.Description)
		t.Sh = strings.TrimSpace(t.Sh)
		t.Expr = strings.TrimSpace(t.Expr)
		t.Js = strings.TrimSpace(t.Js)
		if t.Name == "" {
			return nil, fmt.Errorf("%s: tools[%d].name is required", path, i)
		}
		set := []string{}
		if t.Sh != "" {
			set = append(set, "sh")
		}
		if t.Expr != "" {
			set = append(set, "expr")
		}
		if t.Js != "" {
			set = append(set, "js")
		}
		switch len(set) {
		case 0:
			return nil, fmt.Errorf("%s: tool %q: exactly one of sh, expr, js is required", path, t.Name)
		case 1:
		default:
			return nil, fmt.Errorf("%s: tool %q: only one of sh, expr, js may be set (got %s)", path, t.Name, strings.Join(set, ", "))
		}
		for name, p := range t.Params {
			if err := p.validate(t.Name, name); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		for j := range t.Hooks {
			h := &t.Hooks[j]
			if err := normalizeToolHook(h); err != nil {
				return nil, fmt.Errorf("%s: tool %q hook[%d]: %w", path, t.Name, j, err)
			}
		}
	}

	toolNames := make(map[string]struct{}, len(b.Tools))
	for _, t := range b.Tools {
		toolNames[t.Name] = struct{}{}
	}

	for i := range b.Hooks {
		h := &b.Hooks[i]
		if err := normalizeBotHook(h, toolNames); err != nil {
			return nil, fmt.Errorf("%s: hooks[%d]: %w", path, i, err)
		}
	}

	dir := filepath.Dir(abs)
	for key, ref := range b.Agents {
		if !paramNameRe.MatchString(key) {
			return nil, fmt.Errorf("%s: agent %q: name must match [A-Za-z_][A-Za-z0-9_]*", path, key)
		}
		if _, clash := toolNames[key]; clash {
			return nil, fmt.Errorf("%s: agent %q conflicts with a tool of the same name", path, key)
		}
		ref.File = strings.TrimSpace(ref.File)
		ref.Description = strings.TrimSpace(ref.Description)
		if ref.File == "" {
			return nil, fmt.Errorf("%s: agent %q: file is required", path, key)
		}

		childPath := ref.File
		if !filepath.IsAbs(childPath) {
			childPath = filepath.Join(dir, childPath)
		}
		child, err := loadBot(childPath, visited, depth+1)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", key, err)
		}
		ref.Bot = child
		b.Agents[key] = ref
	}

	return &b, nil
}
