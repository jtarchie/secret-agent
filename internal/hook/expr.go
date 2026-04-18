package hook

import (
	"context"
	"errors"
	"fmt"

	"github.com/expr-lang/expr"
)

// compileExpr compiles an expr-lang program once and returns a runner that
// evaluates it against the env map. The program may call `error("msg")` to
// veto the hook; that call panics with an error value, which expr.Run
// recovers and surfaces as the returned error.
//
// A returned value of nil signals pass-through to the caller. Any other
// value replaces the payload (subject to per-event rules in adapter.go).
func compileExpr(code string) (func(context.Context, map[string]any) (any, error), error) {
	program, err := expr.Compile(code, expr.AllowUndefinedVariables())
	if err != nil {
		return nil, fmt.Errorf("compile expr: %w", err)
	}

	return func(_ context.Context, env map[string]any) (any, error) {
		// Shallow-copy so the error() binding doesn't leak across calls.
		bindings := make(map[string]any, len(env)+1)
		for k, v := range env {
			bindings[k] = v
		}
		bindings["error"] = func(msg string) any {
			panic(errors.New(msg))
		}
		return expr.Run(program, bindings)
	}, nil
}
