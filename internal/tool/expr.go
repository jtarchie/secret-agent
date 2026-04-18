package tool

import (
	"encoding/json"
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
)

// NewExpr returns an ADK tool that evaluates an expr-lang expression in an
// isolated in-process sandbox. Declared params are bound as top-level
// variables; attachment params resolve to the local file path string.
// The expression's result is JSON-marshaled into shellResult.Output.
func NewExpr(name, description, code string, params map[string]bot.Param) (adktool.Tool, error) {
	program, err := expr.Compile(code, expr.AllowUndefinedVariables())
	if err != nil {
		return nil, fmt.Errorf("compile expr: %w", err)
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
			env, err := buildRuntimeEnv(name, params, args, AttachmentsFromContext(ctx))
			if err != nil {
				return shellResult{}, err
			}

			out, err := runExpr(program, env)
			if err != nil {
				return shellResult{}, fmt.Errorf("%s: %w", name, err)
			}

			b, err := json.Marshal(out)
			if err != nil {
				return shellResult{}, fmt.Errorf("%s: marshal result: %w", name, err)
			}
			return shellResult{Output: string(b)}, nil
		},
	)
}

func runExpr(program *vm.Program, env map[string]any) (any, error) {
	return expr.Run(program, env)
}

// buildRuntimeEnv maps LLM-supplied args to variable bindings for expr/js
// runtimes. Attachments resolve to their local path; scalars pass through
// with sloppiness-tolerant coercion to their declared type.
func buildRuntimeEnv(toolName string, params map[string]bot.Param, args map[string]any, atts []chat.Attachment) (map[string]any, error) {
	env := make(map[string]any, len(params))
	for paramName, p := range params {
		value, ok := args[paramName]
		if !ok || value == nil {
			if p.Default != nil {
				value = p.Default
			} else {
				continue
			}
		}

		if p.Type == bot.ParamAttachment {
			path, err := resolveAttachment(value, atts)
			if err != nil {
				return nil, fmt.Errorf("%s: param %q: %w", toolName, paramName, err)
			}
			env[paramName] = path
			continue
		}

		coerced, err := coerceRuntimeValue(value, p.Type)
		if err != nil {
			return nil, fmt.Errorf("%s: param %q: %w", toolName, paramName, err)
		}
		env[paramName] = coerced
	}
	return env, nil
}
