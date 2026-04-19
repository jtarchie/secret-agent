package eval

import (
	"testing"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/runtime"
)

func TestEvaluateExpect_ToolTrajectorySubsequence(t *testing.T) {
	t.Parallel()

	expect := bot.TestExpect{
		ToolCalls: []bot.ExpectedToolCall{
			{Name: "greet", Args: map[string]any{"who": "Ada"}},
			{Name: "shout"},
		},
	}

	// Extra calls between matches are allowed; order within expected is enforced.
	actual := []runtime.ToolCall{
		{Name: "current_time"},
		{Name: "greet", Args: map[string]any{"who": "Ada"}},
		{Name: "add"},
		{Name: "shout", Args: map[string]any{"who": "bob"}},
	}
	if fails := evaluateExpect(expect, actual, ""); len(fails) != 0 {
		t.Fatalf("expected pass, got failures: %v", fails)
	}
}

func TestEvaluateExpect_ToolTrajectoryWrongOrderFails(t *testing.T) {
	t.Parallel()

	expect := bot.TestExpect{
		ToolCalls: []bot.ExpectedToolCall{
			{Name: "first"},
			{Name: "second"},
		},
	}
	actual := []runtime.ToolCall{
		{Name: "second"},
		{Name: "first"},
	}
	fails := evaluateExpect(expect, actual, "")
	if len(fails) == 0 {
		t.Fatal("expected trajectory failure, got none")
	}
}

func TestEvaluateExpect_ArgsSubsetMatching(t *testing.T) {
	t.Parallel()

	// Expected args a:17 must appear with equal value; extra actual args (b, mode) are fine.
	expect := bot.TestExpect{
		ToolCalls: []bot.ExpectedToolCall{{Name: "add", Args: map[string]any{"a": 17}}},
	}
	actual := []runtime.ToolCall{
		{Name: "add", Args: map[string]any{"a": float64(17), "b": float64(25), "mode": "int"}},
	}
	if fails := evaluateExpect(expect, actual, ""); len(fails) != 0 {
		t.Fatalf("expected pass with subset args, got: %v", fails)
	}
}

func TestEvaluateExpect_ArgsMismatchFails(t *testing.T) {
	t.Parallel()

	expect := bot.TestExpect{
		ToolCalls: []bot.ExpectedToolCall{{Name: "add", Args: map[string]any{"a": 17}}},
	}
	actual := []runtime.ToolCall{
		{Name: "add", Args: map[string]any{"a": float64(99)}},
	}
	fails := evaluateExpect(expect, actual, "")
	if len(fails) == 0 {
		t.Fatal("expected args mismatch failure, got none")
	}
}

func TestEvaluateExpect_FinalOutputMatchers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		matcher bot.OutputMatcher
		got     string
		wantOK  bool
	}{
		{"equals pass", bot.OutputMatcher{Equals: "hi"}, "hi", true},
		{"equals fail", bot.OutputMatcher{Equals: "hi"}, "hello", false},
		{"contains pass", bot.OutputMatcher{Contains: []string{"Ada", "!"}}, "Hello, Ada!", true},
		{"contains fail", bot.OutputMatcher{Contains: []string{"Bob"}}, "Hello, Ada!", false},
		{"not_contains pass", bot.OutputMatcher{NotContains: []string{"error"}}, "Hello, Ada!", true},
		{"not_contains fail", bot.OutputMatcher{NotContains: []string{"Ada"}}, "Hello, Ada!", false},
		{"regex pass", bot.OutputMatcher{Regex: `\d+`}, "answer is 42", true},
		{"regex fail", bot.OutputMatcher{Regex: `\d+`}, "no numbers", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fails := evaluateExpect(bot.TestExpect{FinalOutput: &tc.matcher}, nil, tc.got)
			gotOK := len(fails) == 0
			if gotOK != tc.wantOK {
				t.Fatalf("wantOK=%v, got fails=%v", tc.wantOK, fails)
			}
		})
	}
}

func TestScalarEqual_NumericCoercion(t *testing.T) {
	t.Parallel()
	// YAML may parse 17 as int; runtime observes float64.
	if !scalarEqual(17, float64(17)) {
		t.Fatal("int/float64 should compare equal")
	}
	if !scalarEqual(int64(17), float64(17)) {
		t.Fatal("int64/float64 should compare equal")
	}
	if scalarEqual(17, float64(18)) {
		t.Fatal("different numbers should not compare equal")
	}
	if !scalarEqual("hi", "hi") {
		t.Fatal("equal strings should compare equal")
	}
	if scalarEqual("hi", 1) {
		t.Fatal("string vs number should not compare equal")
	}
}
