// Package eval runs declarative test cases (Bot.Tests) against a bot.
//
// Each case sends Input as a single user turn to a fresh in-memory session,
// captures the tool-call trajectory via a runtime ToolRecorder, accumulates
// the model's text output, and scores the turn against the case's Expect
// block. The LLM must be reachable — tests exercise real model decisions.
package eval

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	adkmodel "google.golang.org/adk/model"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/runtime"
)

// Result is the outcome of a single test case.
type Result struct {
	Name      string
	Passed    bool
	Failures  []string
	Duration  time.Duration
	ToolCalls []runtime.ToolCall
	FinalText string
}

// RunAll executes every test in b.Tests sequentially and returns one Result
// per case. The returned error is non-nil only for setup failures (bad bot,
// LLM unreachable) — a test that fails its assertions returns Passed=false
// in its Result, not an error.
func RunAll(ctx context.Context, b *bot.Bot, llm adkmodel.LLM) ([]Result, error) {
	results := make([]Result, 0, len(b.Tests))
	for _, tc := range b.Tests {
		r, err := runCase(ctx, b, llm, tc)
		if err != nil {
			return results, fmt.Errorf("test %q: %w", tc.Name, err)
		}
		results = append(results, r)
	}
	return results, nil
}

// runCase runs one test case end-to-end: stands up a fresh runtime with a
// trajectory recorder, dispatches Input as a single turn, waits for the
// reply stream to drain, and evaluates the expectations.
func runCase(ctx context.Context, b *bot.Bot, llm adkmodel.LLM, tc bot.TestCase) (Result, error) {
	res := Result{Name: tc.Name}

	var (
		mu    sync.Mutex
		calls []runtime.ToolCall
	)
	rec := func(c runtime.ToolCall) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, c)
	}

	rt, err := runtime.New(ctx, b, llm, runtime.WithToolRecorder(rec))
	if err != nil {
		return res, fmt.Errorf("build runtime: %w", err)
	}

	handler := rt.HandlerFor("eval#" + sanitize(tc.Name))

	start := time.Now()
	var reply strings.Builder
	var streamErr error
	for ch := range handler(ctx, chat.Message{Text: tc.Input}) {
		if ch.Err != nil {
			streamErr = ch.Err
			continue
		}
		reply.WriteString(ch.Delta)
	}
	res.Duration = time.Since(start)
	res.FinalText = reply.String()

	mu.Lock()
	res.ToolCalls = append(res.ToolCalls, calls...)
	mu.Unlock()

	if streamErr != nil {
		res.Failures = append(res.Failures, fmt.Sprintf("stream error: %v", streamErr))
	}

	res.Failures = append(res.Failures, evaluateExpect(tc.Expect, res.ToolCalls, res.FinalText)...)
	res.Passed = len(res.Failures) == 0
	return res, nil
}

// evaluateExpect produces one failure string per unmet assertion; an empty
// slice means all assertions held.
func evaluateExpect(e bot.TestExpect, calls []runtime.ToolCall, finalText string) []string {
	var fails []string

	if len(e.ToolCalls) > 0 {
		if msg, ok := matchToolTrajectory(e.ToolCalls, calls); !ok {
			fails = append(fails, msg)
		}
	}

	if e.FinalOutput != nil {
		fails = append(fails, matchOutput(*e.FinalOutput, finalText)...)
	}

	return fails
}

// matchToolTrajectory checks that each expected call appears, in order, as
// a subsequence of the actual trajectory. Args match as a subset: every
// listed key must appear in the actual args with an equal value; extra
// actual args are ignored.
func matchToolTrajectory(expected []bot.ExpectedToolCall, actual []runtime.ToolCall) (string, bool) {
	i := 0
	for _, ac := range actual {
		if i >= len(expected) {
			break
		}
		if toolCallMatches(expected[i], ac) {
			i++
		}
	}
	if i == len(expected) {
		return "", true
	}
	return fmt.Sprintf("tool trajectory mismatch: matched %d/%d expected calls\n  expected: %s\n  actual:   %s",
		i, len(expected), formatExpected(expected), formatActual(actual)), false
}

func toolCallMatches(exp bot.ExpectedToolCall, got runtime.ToolCall) bool {
	if exp.Name != got.Name {
		return false
	}
	for k, v := range exp.Args {
		gv, ok := got.Args[k]
		if !ok {
			return false
		}
		if !scalarEqual(v, gv) {
			return false
		}
	}
	return true
}

// scalarEqual compares values leniently across YAML/JSON number types so
// `args: { a: 2 }` matches an observed float64(2). Falls back to
// reflect.DeepEqual for strings, bools, and any nested structure.
func scalarEqual(want, got any) bool {
	if reflect.DeepEqual(want, got) {
		return true
	}
	wf, wOk := toFloat(want)
	gf, gOk := toFloat(got)
	if wOk && gOk {
		return wf == gf
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	}
	return 0, false
}

func matchOutput(m bot.OutputMatcher, got string) []string {
	var fails []string
	if m.Equals != "" && got != m.Equals {
		fails = append(fails, fmt.Sprintf("final_output.equals: want %q, got %q", m.Equals, got))
	}
	for _, s := range m.Contains {
		if !strings.Contains(got, s) {
			fails = append(fails, fmt.Sprintf("final_output.contains: missing %q in %q", s, got))
		}
	}
	for _, s := range m.NotContains {
		if strings.Contains(got, s) {
			fails = append(fails, fmt.Sprintf("final_output.not_contains: found %q in %q", s, got))
		}
	}
	if m.Regex != "" {
		re, err := regexp.Compile(m.Regex)
		if err != nil {
			fails = append(fails, fmt.Sprintf("final_output.regex: compile: %v", err))
		} else if !re.MatchString(got) {
			fails = append(fails, fmt.Sprintf("final_output.regex: no match for /%s/ in %q", m.Regex, got))
		}
	}
	return fails
}

func formatExpected(es []bot.ExpectedToolCall) string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		if len(e.Args) == 0 {
			parts = append(parts, e.Name)
		} else {
			parts = append(parts, fmt.Sprintf("%s(%v)", e.Name, e.Args))
		}
	}
	if len(parts) == 0 {
		return "[]"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatActual(cs []runtime.ToolCall) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, c.Name)
	}
	if len(parts) == 0 {
		return "[]"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

var sanitizeRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func sanitize(s string) string {
	return sanitizeRe.ReplaceAllString(s, "_")
}
