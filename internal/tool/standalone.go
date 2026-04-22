package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/dop251/goja"
	"github.com/expr-lang/expr"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// RunShellScript executes a standalone shell script under mvdan.cc/sh with
// the process environment, returning captured stdout. It is intended for
// non-tool execution paths (e.g. the cron scheduler) where no params,
// attachments, or sender identity are available.
func RunShellScript(ctx context.Context, script, name string) (string, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), name)
	if err != nil {
		return "", fmt.Errorf("%s: parse script: %w", name, err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(
		interp.Env(expand.ListEnviron(os.Environ()...)),
		interp.StdIO(nil, &stdout, &stderr),
	)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	err = runner.Run(ctx, file)
	if err != nil {
		return "", fmt.Errorf("%s: %w (stderr: %s)", name, err, stderr.String())
	}
	return stdout.String(), nil
}

// RunExpr evaluates an expr-lang expression with no bindings and returns
// the JSON-marshaled result as a string.
func RunExpr(_ context.Context, code, name string) (string, error) {
	program, err := expr.Compile(code, expr.AllowUndefinedVariables())
	if err != nil {
		return "", fmt.Errorf("%s: compile expr: %w", name, err)
	}
	v, err := expr.Run(program, map[string]any{})
	if err != nil {
		return "", fmt.Errorf("%s: expr run: %w", name, err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%s: marshal result: %w", name, err)
	}
	return string(b), nil
}

// RunJs evaluates a JavaScript program in a fresh goja VM with no bindings
// and returns the JSON-marshaled completion value as a string. The VM is
// interrupted if ctx is cancelled.
func RunJs(ctx context.Context, code, name string) (string, error) {
	program, err := goja.Compile(name, code, true)
	if err != nil {
		return "", fmt.Errorf("%s: compile js: %w", name, err)
	}
	vm := goja.New()
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
		return "", fmt.Errorf("%s: %w", name, err)
	}
	var out any
	if val != nil && !goja.IsUndefined(val) && !goja.IsNull(val) {
		out = val.Export()
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("%s: marshal result: %w", name, err)
	}
	return string(b), nil
}
