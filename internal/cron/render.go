package cron

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/tool"
)

// MaxScriptOutputBytes caps the .Output field exposed to a hybrid cron
// prompt template. Anything longer is truncated with a marker so the
// model's context window can't be blown by a chatty script.
const MaxScriptOutputBytes = 16 * 1024

// scriptResult is the template data exposed to a hybrid cron's prompt.
// Field names are exported so text/template actions like {{.Output}}
// resolve via reflection.
type scriptResult struct {
	Output   string
	Err      string
	ExitCode int
}

// runCronScript executes whichever of entry.Sh / entry.Expr / entry.Js is
// set and returns the result. Callers must have established that exactly
// one script field is non-empty. Output is truncated before return.
func runCronScript(ctx context.Context, entry bot.Cron, senders chat.SenderRegistry) scriptResult {
	var (
		out string
		err error
	)
	switch {
	case entry.Sh != "":
		out, err = tool.RunShellScript(ctx, entry.Sh, entry.Name, senders)
	case entry.Expr != "":
		out, err = tool.RunExpr(ctx, entry.Expr, entry.Name, senders)
	case entry.Js != "":
		out, err = tool.RunJs(ctx, entry.Js, entry.Name, senders)
	}
	res := scriptResult{Output: truncateOutput(out)}
	if err != nil {
		res.Err = err.Error()
		res.ExitCode = 1
	}
	return res
}

func truncateOutput(s string) string {
	if len(s) <= MaxScriptOutputBytes {
		return s
	}
	dropped := len(s) - MaxScriptOutputBytes
	return s[:MaxScriptOutputBytes] + fmt.Sprintf("\n…[truncated %d bytes]", dropped)
}

// renderPromptTemplate parses entry.Prompt and executes it against res.
// Parse and execution errors both surface to the caller; the scheduler
// logs them via makeJob's existing error path.
func renderPromptTemplate(entry bot.Cron, res scriptResult) (string, error) {
	tpl, err := template.New(entry.Name).Parse(entry.Prompt)
	if err != nil {
		return "", fmt.Errorf("parse prompt template: %w", err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, res); err != nil {
		return "", fmt.Errorf("render prompt template: %w", err)
	}
	return buf.String(), nil
}
