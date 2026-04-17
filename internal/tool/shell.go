// Package tool adapts YAML-defined bot tools into ADK tool.Tool values.
package tool

import (
	"bytes"
	"fmt"
	"strings"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// noArgs is the parameter struct for a shell tool. It carries no fields; the
// script is fixed at registration time and the LLM only chooses whether to
// invoke the tool.
type noArgs struct{}

type shellResult struct {
	Output string `json:"output"`
}

// NewShell returns an ADK tool that executes the given shell script using the
// pure-Go mvdan.cc/sh interpreter and yields stdout.
func NewShell(name, description, script string) (adktool.Tool, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), name)
	if err != nil {
		return nil, fmt.Errorf("parse script: %w", err)
	}

	return functiontool.New(
		functiontool.Config{Name: name, Description: description},
		func(ctx adktool.Context, _ noArgs) (shellResult, error) {
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.StdIO(nil, &stdout, &stderr))
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
