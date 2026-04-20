package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/eval"
	"github.com/jtarchie/secret-agent/internal/model"
)

// EvalCmd runs a bot's `tests:` block as an offline eval.
type EvalCmd struct {
	ModelFlags
	Verbose bool   `help:"print observed tool-call trajectory and final text for each case"`
	Bot     string `arg:""                                                                  help:"path to the bot YAML with a tests: block" type:"existingfile"`
}

//nolint:cyclop // eval result reporting is sequential and clearer as one function
func (c *EvalCmd) Run() error {
	b, err := bot.Load(c.Bot)
	if err != nil {
		return fmt.Errorf("load bot: %w", err)
	}
	if len(b.Tests) == 0 {
		return fmt.Errorf("%s: no `tests:` block declared", c.Bot)
	}

	provider, name := model.SplitModel(c.Model)
	llm, err := model.Resolve(provider, name, c.APIKey, c.BaseURL)
	if err != nil {
		return fmt.Errorf("resolve model: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !c.SkipPreflight {
		preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := model.Preflight(preflightCtx, provider, c.APIKey, c.BaseURL)
		cancel()
		if err != nil {
			return fmt.Errorf("model preflight failed (use --skip-preflight to bypass): %w", err)
		}
	}

	results, err := eval.RunAll(ctx, b, llm, eval.WithOnStart(func(name string) {
		fmt.Printf("RUN   %s\n", name)
	}))
	if err != nil {
		return fmt.Errorf("run eval: %w", err)
	}

	passed, failed := 0, 0
	for _, r := range results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
			failed++
		} else {
			passed++
		}
		fmt.Printf("%s  %s  (%s)\n", status, r.Name, r.Duration.Round(time.Millisecond))
		if c.Verbose || !r.Passed {
			if len(r.ToolCalls) > 0 {
				names := make([]string, len(r.ToolCalls))
				for i, c := range r.ToolCalls {
					names[i] = c.Name
				}
				fmt.Printf("      tools: %v\n", names)
			}
			if r.FinalText != "" {
				fmt.Printf("      output: %q\n", r.FinalText)
			}
		}
		for _, f := range r.Failures {
			fmt.Printf("      - %s\n", f)
		}
	}
	fmt.Printf("\n%d passed, %d failed (of %d)\n", passed, failed, len(results))
	if failed > 0 {
		return fmt.Errorf("%d test(s) failed", failed)
	}
	return nil
}
