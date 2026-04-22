// Package cron wires a bot's `cron:` entries to a background scheduler so
// directives fire on their declared cadence without requiring an incoming
// user message. Each entry either runs a synthetic prompt through the
// bot's agent or executes a sh/expr/js script directly.
package cron

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	robcron "github.com/robfig/cron/v3"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/runtime"
	"github.com/jtarchie/secret-agent/internal/tool"
)

// Runner is the minimal surface a Scheduler needs from a *runtime.Runtime.
// Accepting it as an interface keeps scheduler tests from having to stand
// up a real ADK runner.
type Runner interface {
	HandlerFor(convID string) func(context.Context, chat.Message) <-chan chat.Chunk
}

// Scheduler fires cron directives for one or more bots on their declared
// cadences. Construct with New, stage entries via Register, then call Run
// from a goroutine — it blocks until its context is cancelled.
type Scheduler struct {
	logger *slog.Logger
	c      *robcron.Cron
	jobs   int
}

// New returns a Scheduler with no staged jobs.
func New(logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		logger: logger,
		c:      robcron.New(),
	}
}

// HasJobs reports whether any cron entries have been registered.
func (s *Scheduler) HasJobs() bool {
	return s.jobs > 0
}

// Register schedules every entry in b.Cron against rt. Each entry's schedule
// has already been validated by bot.Load; this method re-parses it for
// registration. It returns an error only if re-parse unexpectedly fails.
func (s *Scheduler) Register(b *bot.Bot, rt Runner) error {
	for i := range b.Cron {
		entry := b.Cron[i]
		sched, err := buildSchedule(entry)
		if err != nil {
			return fmt.Errorf("bot %q cron %q: %w", b.Name, entry.Name, err)
		}
		logger := s.logger.With(
			"component", "cron",
			"bot", b.Name,
			"cron", entry.Name,
		)
		job := makeJob(entry, b.Name, rt, logger)
		wrapped := robcron.NewChain(robcron.SkipIfStillRunning(slogPrintfAdapter{logger})).Then(job)
		s.c.Schedule(sched, wrapped)
		s.jobs++
	}
	return nil
}

// Run starts the scheduler and blocks until ctx is cancelled. On cancel it
// stops accepting new fires and waits for in-flight jobs to complete.
func (s *Scheduler) Run(ctx context.Context) error {
	s.c.Start()
	<-ctx.Done()
	stopCtx := s.c.Stop()
	<-stopCtx.Done()
	return nil
}

// buildSchedule turns a validated Cron entry into a robcron.Schedule.
func buildSchedule(c bot.Cron) (robcron.Schedule, error) {
	switch {
	case c.Schedule != "":
		parser := robcron.NewParser(robcron.Minute | robcron.Hour | robcron.Dom | robcron.Month | robcron.Dow)
		sched, err := parser.Parse(c.Schedule)
		if err != nil {
			return nil, fmt.Errorf("parse schedule: %w", err)
		}
		return sched, nil
	case c.Every != "":
		d, err := time.ParseDuration(c.Every)
		if err != nil {
			return nil, fmt.Errorf("parse every: %w", err)
		}
		return robcron.Every(d), nil
	}
	return nil, fmt.Errorf("no schedule or every set")
}

// makeJob returns the robcron.Job closure that runs when the cadence fires.
func makeJob(entry bot.Cron, botName string, rt Runner, logger *slog.Logger) robcron.Job {
	mode, trigger := describe(entry)
	return robcron.FuncJob(func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start := time.Now()
		logger.Debug("fire", "mode", mode, "trigger", trigger)
		bytesOut, err := runEntry(ctx, entry, botName, rt)
		dur := time.Since(start)
		if err != nil {
			logger.Error("fire failed",
				"mode", mode,
				"duration_ms", dur.Milliseconds(),
				"err", err,
			)
			return
		}
		logger.Info("fired",
			"mode", mode,
			"duration_ms", dur.Milliseconds(),
			"bytes_out", bytesOut,
		)
	})
}

func describe(c bot.Cron) (mode, trigger string) {
	switch {
	case c.Prompt != "":
		mode = "prompt"
	case c.Sh != "":
		mode = "sh"
	case c.Expr != "":
		mode = "expr"
	case c.Js != "":
		mode = "js"
	}
	if c.Schedule != "" {
		trigger = "schedule"
	} else {
		trigger = "every"
	}
	return mode, trigger
}

// runEntry dispatches the entry to its executor and returns the number of
// output bytes produced.
func runEntry(ctx context.Context, entry bot.Cron, botName string, rt Runner) (int, error) {
	switch {
	case entry.Prompt != "":
		return runPrompt(ctx, entry, botName, rt)
	case entry.Sh != "":
		out, err := tool.RunShellScript(ctx, entry.Sh, entry.Name)
		return len(out), err
	case entry.Expr != "":
		out, err := tool.RunExpr(ctx, entry.Expr, entry.Name)
		return len(out), err
	case entry.Js != "":
		out, err := tool.RunJs(ctx, entry.Js, entry.Name)
		return len(out), err
	}
	return 0, fmt.Errorf("cron entry %q has no directive", entry.Name)
}

// runPrompt injects a synthetic user message into the runtime's per-conv
// handler. The convID is stable across fires so `memory: full` bots
// accumulate context — `memory: none` bots get a fresh session per turn
// via runtime's existing stateless branch.
func runPrompt(ctx context.Context, entry bot.Cron, botName string, rt Runner) (int, error) {
	convID := fmt.Sprintf("cron:%s:%s", botName, entry.Name)
	handler := rt.HandlerFor(convID)
	out := handler(ctx, chat.Message{Text: entry.Prompt})
	total := 0
	for chunk := range out {
		if chunk.Err != nil {
			return total, chunk.Err
		}
		total += len(chunk.Delta)
	}
	return total, nil
}

// Ensure *runtime.Runtime satisfies the Runner interface.
var _ Runner = (*runtime.Runtime)(nil)

// slogPrintfAdapter shims an *slog.Logger onto the Printf-style Logger
// interface that robfig/cron's SkipIfStillRunning wrapper expects.
type slogPrintfAdapter struct{ l *slog.Logger }

func (a slogPrintfAdapter) Info(msg string, keysAndValues ...any) {
	a.l.Info(msg, keysAndValues...)
}
func (a slogPrintfAdapter) Error(err error, msg string, keysAndValues ...any) {
	a.l.Error(msg, append([]any{"err", err}, keysAndValues...)...)
}
