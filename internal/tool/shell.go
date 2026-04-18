// Package tool adapts YAML-defined bot tools into ADK tool.Tool values.
package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/jtarchie/secret-agent/internal/bot"
)

type shellResult struct {
	Output string `json:"output"`
}

// NewShell returns an ADK tool that executes the given shell script using the
// pure-Go mvdan.cc/sh interpreter and yields stdout. Declared params are
// injected as environment variables before the script runs.
func NewShell(name, description, script string, params map[string]bot.Param) (adktool.Tool, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), name)
	if err != nil {
		return nil, fmt.Errorf("parse script: %w", err)
	}

	schema, err := buildSchema(params)
	if err != nil {
		return nil, fmt.Errorf("build schema: %w", err)
	}

	return functiontool.New(
		functiontool.Config{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
		func(ctx adktool.Context, args map[string]any) (shellResult, error) {
			env := os.Environ()
			atts := AttachmentsFromContext(ctx)
			for paramName, p := range params {
				value, ok := args[paramName]
				if !ok || value == nil {
					if p.Default != nil {
						value = p.Default
					} else {
						continue
					}
				}
				var (
					s   string
					err error
				)
				if p.Type == bot.ParamAttachment {
					s, err = resolveAttachment(value, atts)
				} else {
					s, err = toEnvString(value, p.Type)
				}
				if err != nil {
					return shellResult{}, fmt.Errorf("%s: param %q: %w", name, paramName, err)
				}
				env = append(env, paramName+"="+s)
			}

			var stdout, stderr bytes.Buffer
			runner, err := interp.New(
				interp.Env(expand.ListEnviron(env...)),
				interp.StdIO(nil, &stdout, &stderr),
			)
			if err != nil {
				return shellResult{}, fmt.Errorf("%s: %w", name, err)
			}
			if err := runner.Run(ctx, file); err != nil {
				return shellResult{}, fmt.Errorf("%s: %w (stderr: %s)", name, err, stderr.String())
			}
			return shellResult{Output: stdout.String()}, nil
		},
	)
}

func buildSchema(params map[string]bot.Param) (*jsonschema.Schema, error) {
	s := &jsonschema.Schema{Type: "object"}
	if len(params) == 0 {
		return s, nil
	}
	s.Properties = make(map[string]*jsonschema.Schema, len(params))
	for name, p := range params {
		schemaType := string(p.Type)
		desc := p.Description
		if p.Type == bot.ParamAttachment {
			schemaType = "string"
			if desc == "" {
				desc = "an attachment from the current turn"
			}
			desc += " — pass the attachment index (e.g. \"0\") or filename"
		}
		prop := &jsonschema.Schema{
			Type:        schemaType,
			Description: desc,
			Enum:        p.Enum,
		}
		if p.Default != nil {
			b, err := json.Marshal(p.Default)
			if err != nil {
				return nil, fmt.Errorf("param %q default: %w", name, err)
			}
			prop.Default = b
		}
		s.Properties[name] = prop
		if p.Required {
			s.Required = append(s.Required, name)
		}
	}
	return s, nil
}

// toEnvString coerces an LLM-supplied value into a shell-env string, being
// lenient about JSON type sloppiness (accepts "2" for an integer param, etc.).
func toEnvString(v any, t bot.ParamType) (string, error) {
	switch t {
	case bot.ParamString:
		switch x := v.(type) {
		case string:
			return x, nil
		default:
			return fmt.Sprintf("%v", x), nil
		}
	case bot.ParamInteger:
		switch x := v.(type) {
		case float64:
			return strconv.FormatInt(int64(x), 10), nil
		case int:
			return strconv.Itoa(x), nil
		case int64:
			return strconv.FormatInt(x, 10), nil
		case string:
			n, err := strconv.ParseInt(x, 10, 64)
			if err != nil {
				return "", fmt.Errorf("cannot parse %q as integer", x)
			}
			return strconv.FormatInt(n, 10), nil
		default:
			return "", fmt.Errorf("expected integer, got %T", v)
		}
	case bot.ParamNumber:
		switch x := v.(type) {
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64), nil
		case int:
			return strconv.Itoa(x), nil
		case int64:
			return strconv.FormatInt(x, 10), nil
		case string:
			n, err := strconv.ParseFloat(x, 64)
			if err != nil {
				return "", fmt.Errorf("cannot parse %q as number", x)
			}
			return strconv.FormatFloat(n, 'f', -1, 64), nil
		default:
			return "", fmt.Errorf("expected number, got %T", v)
		}
	case bot.ParamBoolean:
		switch x := v.(type) {
		case bool:
			return strconv.FormatBool(x), nil
		case string:
			b, err := strconv.ParseBool(x)
			if err != nil {
				return "", fmt.Errorf("cannot parse %q as boolean", x)
			}
			return strconv.FormatBool(b), nil
		default:
			return "", fmt.Errorf("expected boolean, got %T", v)
		}
	}
	return "", fmt.Errorf("unknown param type %q", t)
}
