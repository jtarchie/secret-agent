package tool

import (
	"encoding/json"
	"fmt"

	"github.com/dop251/goja"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"github.com/jtarchie/secret-agent/internal/bot"
)

// NewJs returns an ADK tool that evaluates a JavaScript program in an
// isolated goja runtime (no filesystem/network bindings, no require, no
// console). A fresh VM is created per call so no state leaks across
// invocations. Declared params are bound as top-level vars; attachment
// params resolve to the local file path string. The script's completion
// value is JSON-marshaled into shellResult.Output.
//
// Script execution is interrupted when the tool call's context is canceled,
// giving the ADK call timeout a hard kill switch against runaway scripts.
func NewJs(name, description, code string, params map[string]bot.Param) (adktool.Tool, error) {
	program, err := goja.Compile(name, code, true)
	if err != nil {
		return nil, fmt.Errorf("compile js: %w", err)
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
			env, err := buildRuntimeEnv(name, params, args, AttachmentsFromContext(ctx), SenderPhoneFromContext(ctx))
			if err != nil {
				return shellResult{}, err
			}

			vm := goja.New()
			for k, v := range env {
				if err := vm.Set(k, v); err != nil {
					return shellResult{}, fmt.Errorf("%s: bind %q: %w", name, k, err)
				}
			}

			done := make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-ctx.Done():
					vm.Interrupt(ctx.Err())
				case <-done:
				}
			}()

			val, err := vm.RunProgram(program)
			if err != nil {
				return shellResult{}, fmt.Errorf("%s: %w", name, err)
			}

			var out any
			if val != nil && !goja.IsUndefined(val) && !goja.IsNull(val) {
				out = val.Export()
			}

			b, err := json.Marshal(out)
			if err != nil {
				return shellResult{}, fmt.Errorf("%s: marshal result: %w", name, err)
			}
			return shellResult{Output: string(b)}, nil
		},
	)
}
