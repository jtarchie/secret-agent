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

	"github.com/jtarchie/secret-agent/internal/chat"
)

// RunShellScript executes a standalone shell script under mvdan.cc/sh with
// the process environment, returning captured stdout. It is intended for
// non-tool execution paths (e.g. the cron scheduler) where no params,
// attachments, or sender identity are available. When senders is non-nil,
// the `sa_send` shell builtin is available to dispatch outbound messages.
func RunShellScript(ctx context.Context, script, name string, senders chat.SenderRegistry) (string, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), name)
	if err != nil {
		return "", fmt.Errorf("%s: parse script: %w", name, err)
	}
	var stdout, stderr bytes.Buffer
	opts := []interp.RunnerOption{
		interp.Env(expand.ListEnviron(os.Environ()...)),
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

// RunExpr evaluates an expr-lang expression and returns the JSON-marshaled
// result as a string. When senders is non-nil, a `send_message(transport,
// to, body)` function is bound into the evaluation env.
func RunExpr(ctx context.Context, code, name string, senders chat.SenderRegistry) (string, error) {
	program, err := expr.Compile(code, expr.AllowUndefinedVariables())
	if err != nil {
		return "", fmt.Errorf("%s: compile expr: %w", name, err)
	}
	env := map[string]any{}
	if senders != nil {
		env["send_message"] = func(transport, to, body string) (string, error) {
			dispatchErr := DispatchSend(ctx, senders, transport, to, body)
			return MarshalSendResult(dispatchErr)
		}
	}
	v, err := expr.Run(program, env)
	if err != nil {
		return "", fmt.Errorf("%s: expr run: %w", name, err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%s: marshal result: %w", name, err)
	}
	return string(b), nil
}

// RunJs evaluates a JavaScript program in a fresh goja VM and returns the
// JSON-marshaled completion value as a string. The VM is interrupted if
// ctx is cancelled. When senders is non-nil, a `send_message(transport,
// to, body)` function is bound on the VM.
func RunJs(ctx context.Context, code, name string, senders chat.SenderRegistry) (string, error) {
	program, err := goja.Compile(name, code, true)
	if err != nil {
		return "", fmt.Errorf("%s: compile js: %w", name, err)
	}
	vm := goja.New()
	if senders != nil {
		err := vm.Set("send_message", func(transport, to, body string) string {
			dispatchErr := DispatchSend(ctx, senders, transport, to, body)
			out, _ := MarshalSendResult(dispatchErr)
			return out
		})
		if err != nil {
			return "", fmt.Errorf("%s: bind send_message: %w", name, err)
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
