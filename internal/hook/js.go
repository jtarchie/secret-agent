package hook

import (
	"context"
	"fmt"

	"github.com/dop251/goja"
)

// compileJs compiles a JS program once and returns a runner that, per call,
// spins up a fresh goja.Runtime, binds env vars, runs the program, and
// exports the completion value. `undefined` and `null` map to a Go nil
// (pass-through). A thrown exception surfaces as an error.
//
// Execution is interrupted when ctx is cancelled, mirroring the tool/js
// behavior.
func compileJs(code string) (func(context.Context, map[string]any) (any, error), error) {
	program, err := goja.Compile("hook", code, true)
	if err != nil {
		return nil, fmt.Errorf("compile js: %w", err)
	}

	return func(ctx context.Context, env map[string]any) (any, error) {
		vm := goja.New()
		for k, v := range env {
			if err := vm.Set(k, v); err != nil {
				return nil, fmt.Errorf("bind %q: %w", k, err)
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
			return nil, err
		}
		if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
			return nil, nil
		}
		return val.Export(), nil
	}, nil
}
