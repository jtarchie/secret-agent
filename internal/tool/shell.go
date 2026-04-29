// Package tool adapts YAML-defined bot tools into ADK tool.Tool values.
package tool

import (
	"bytes"
	"encoding/json"
	"errors"
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
	"github.com/jtarchie/secret-agent/internal/chat"
)

type shellResult struct {
	Output string `json:"output"`
}

// NewShell returns an ADK tool that executes the given shell script using the
// pure-Go mvdan.cc/sh interpreter and yields stdout. Declared params are
// injected as environment variables before the script runs.
//
// returns: when set to "markdown", the tool's stdout is post-processed
// through an HTML-to-Markdown converter before being handed back to the
// LLM. Empty = passthrough.
//
// senders, when non-nil, installs the `sa_send` shell builtin so tool
// bodies can dispatch outbound messages through any configured transport.
func NewShell(name, description, script string, params map[string]bot.Param, returns string, senders chat.SenderRegistry) (adktool.Tool, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), name)
	if err != nil {
		return nil, fmt.Errorf("parse script: %w", err)
	}

	schema, err := buildSchema(params)
	if err != nil {
		return nil, fmt.Errorf("build schema: %w", err)
	}

	tool, err := functiontool.New(
		functiontool.Config{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
		func(ctx adktool.Context, args map[string]any) (shellResult, error) {
			env, err := buildShellEnv(ctx, args, params, name)
			if err != nil {
				return shellResult{}, err
			}
			output, err := runShellScript(ctx, file, env, name, senders)
			if err != nil {
				return shellResult{}, err
			}
			output, err = postProcessShellOutput(output, returns, name)
			if err != nil {
				return shellResult{}, err
			}
			return shellResult{Output: output}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("new shell tool: %w", err)
	}
	return tool, nil
}

// buildShellEnv assembles the env var slice for a shell tool invocation:
// inherited process env, per-turn sender/conv identity, and param values.
func buildShellEnv(ctx adktool.Context, args map[string]any, params map[string]bot.Param, name string) ([]string, error) {
	env := os.Environ()
	if phone := SenderPhoneFromContext(ctx); phone != "" {
		env = append(env, "SENDER_PHONE="+phone)
	}
	if id := SenderIDFromContext(ctx); id != "" {
		env = append(env, "SENDER_ID="+id)
	}
	if tr := SenderTransportFromContext(ctx); tr != "" {
		env = append(env, "SENDER_TRANSPORT="+tr)
	}
	if cid := ConvIDFromContext(ctx); cid != "" {
		env = append(env, "CONV_ID="+cid)
	}
	atts := AttachmentsFromContext(ctx)
	for paramName, p := range params {
		entries, err := paramEnvEntries(paramName, p, args, atts, name)
		if err != nil {
			return nil, err
		}
		env = append(env, entries...)
	}
	return env, nil
}

// paramEnvEntries returns the env var assignments for a single declared
// param. Attachment params resolve against the turn's attachments;
// markdown params also emit a companion <name>_html entry.
func paramEnvEntries(paramName string, p bot.Param, args map[string]any, atts []chat.Attachment, name string) ([]string, error) {
	value, ok := args[paramName]
	if !ok || value == nil {
		if p.Default == nil {
			return nil, nil
		}
		value = p.Default
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
		return nil, fmt.Errorf("%s: param %q: %w", name, paramName, err)
	}
	out := []string{paramName + "=" + s}
	if p.Type == bot.ParamMarkdown {
		html, err := markdownToHTML(s)
		if err != nil {
			return nil, fmt.Errorf("%s: param %q: %w", name, paramName, err)
		}
		out = append(out, paramName+"_html="+html)
	}
	return out, nil
}

// runShellScript executes a parsed shell AST under mvdan.cc/sh with the
// supplied env and returns captured stdout. When senders is non-nil, the
// sa_send builtin is available for dispatching outbound messages.
func runShellScript(ctx adktool.Context, file *syntax.File, env []string, name string, senders chat.SenderRegistry) (string, error) {
	var stdout, stderr bytes.Buffer
	opts := []interp.RunnerOption{
		interp.Env(expand.ListEnviron(env...)),
		interp.StdIO(nil, &stdout, &stderr),
	}
	if senders != nil {
		opts = append(opts, interp.ExecHandlers(SendBuiltinMiddleware(senders)))
	}
	runner, err := interp.New(opts...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	err = runner.Run(ctx, file)
	if err != nil {
		return "", fmt.Errorf("%s: %w (stderr: %s)", name, err, stderr.String())
	}
	return stdout.String(), nil
}

// postProcessShellOutput applies the optional `returns:` transform to raw
// stdout. Empty = passthrough; "markdown" converts HTML to Markdown.
func postProcessShellOutput(output, returns, name string) (string, error) {
	if returns != "markdown" {
		return output, nil
	}
	md, err := htmlToMarkdown(output)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return md, nil
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
	case bot.ParamString, bot.ParamMarkdown:
		return envFormatString(v), nil
	case bot.ParamInteger:
		return envFormatInteger(v)
	case bot.ParamNumber:
		return envFormatNumber(v)
	case bot.ParamBoolean:
		return envFormatBoolean(v)
	case bot.ParamAttachment:
		return "", errors.New("attachment param reached env coercion (should be resolved earlier)")
	}
	return "", fmt.Errorf("unknown param type %q", t)
}

func envFormatString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func envFormatInteger(v any) (string, error) {
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
}

func envFormatNumber(v any) (string, error) {
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
}

func envFormatBoolean(v any) (string, error) {
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
