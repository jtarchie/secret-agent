// Package hook compiles YAML-defined hooks into ADK callbacks.
//
// A hook is a small sh/expr/js script attached to one of ADK's extension
// points: before_tool, after_tool, before_model, after_model, before_agent,
// after_agent. The script receives a typed env map and returns a value
// used to either pass through (nil), replace the payload (a map/value),
// or veto (a script error).
package hook

import (
	"context"
	"errors"
	"fmt"

	"github.com/jtarchie/secret-agent/internal/bot"
)

// Result is the outcome of running a hook script. HasValue reports whether
// the script produced a replacement payload; Value is meaningful only when
// HasValue is true. A zero Result means "pass through" — used by ADK
// callbacks to continue with the original args/result.
type Result struct {
	Value    any
	HasValue bool
}

// PassThrough is the zero Result indicating no payload override.
var PassThrough = Result{}

// NewValue wraps v as a replacement payload. Callers should use this
// rather than constructing Result literals directly so HasValue stays in
// sync with the presence of a value.
func NewValue(v any) Result {
	return Result{Value: v, HasValue: true}
}

// Compiled is a hook ready to run. Filter, when non-empty, restricts the
// hook to a single tool name (tool-scoped hooks get Filter set to the
// owning tool's name; bot-level hooks set it explicitly via `tool:`).
type Compiled struct {
	Event  bot.HookEvent
	Filter string
	run    func(ctx context.Context, env map[string]any) (Result, error)
}

// Run executes the hook's script against the given env. The returned
// Result is PassThrough when the script signaled no replacement; an error
// vetoes the surrounding ADK callback.
func (c Compiled) Run(ctx context.Context, env map[string]any) (Result, error) {
	return c.run(ctx, env)
}

// Compile parses a list of hooks into Compiled form. It dispatches on
// which of sh/expr/js is set per hook.
func Compile(hs []bot.Hook) ([]Compiled, error) {
	out := make([]Compiled, 0, len(hs))
	for i, h := range hs {
		run, err := compileOne(h)
		if err != nil {
			return nil, fmt.Errorf("hook[%d] (%s): %w", i, h.On, err)
		}
		out = append(out, Compiled{Event: h.On, Filter: h.Tool, run: run})
	}
	return out, nil
}

func compileOne(h bot.Hook) (func(context.Context, map[string]any) (Result, error), error) {
	switch {
	case h.Sh != "":
		return compileSh(h.Sh)
	case h.Expr != "":
		return compileExpr(h.Expr)
	case h.Js != "":
		return compileJs(h.Js)
	}
	return nil, errors.New("no runtime (sh/expr/js) set")
}
