package hook

import (
	"context"
	"strings"
	"testing"

	"github.com/jtarchie/secret-agent/internal/bot"
)

func compileOrFail(t *testing.T, h bot.Hook) Compiled {
	t.Helper()
	cs, err := Compile([]bot.Hook{h})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("compile returned %d hooks", len(cs))
	}
	return cs[0]
}

func runOrFail(t *testing.T, c Compiled, env map[string]any) any {
	t.Helper()
	res, err := c.Run(context.Background(), env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.HasValue {
		return nil
	}
	return res.Value
}

// --- expr runtime -------------------------------------------------------

func TestExprPassThroughNil(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Expr: `nil`})
	if got := runOrFail(t, c, nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestExprReturnsMap(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Expr: `{"output": "hello"}`})
	got := runOrFail(t, c, nil)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if m["output"] != "hello" {
		t.Errorf("output = %v", m["output"])
	}
}

func TestExprErrorVetoes(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Expr: `error("blocked")`})
	_, err := c.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error = %v", err)
	}
}

func TestExprReadsEnv(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Expr: `args.name`})
	got := runOrFail(t, c, map[string]any{"args": map[string]any{"name": "world"}})
	if got != "world" {
		t.Errorf("got %v", got)
	}
}

// --- js runtime ---------------------------------------------------------

func TestJsUndefinedIsPassThrough(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookAfterTool, Js: `undefined`})
	if got := runOrFail(t, c, nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestJsReturnsObject(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookAfterTool, Js: `({ output: "ok" })`})
	got := runOrFail(t, c, nil)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if m["output"] != "ok" {
		t.Errorf("output = %v", m["output"])
	}
}

func TestJsThrowVetoes(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Js: `throw new Error("nope")`})
	_, err := c.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v", err)
	}
}

func TestJsReadsEnv(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Js: `tool_name.toUpperCase()`})
	got := runOrFail(t, c, map[string]any{"tool_name": "greet"})
	if got != "GREET" {
		t.Errorf("got %v", got)
	}
}

// --- sh runtime ---------------------------------------------------------

func TestShEmptyStdoutIsPassThrough(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Sh: `:`})
	if got := runOrFail(t, c, nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestShJSONStdoutDecodes(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookAfterTool, Sh: `echo '{"output":"from-sh"}'`})
	got := runOrFail(t, c, nil)
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if m["output"] != "from-sh" {
		t.Errorf("output = %v", m["output"])
	}
}

func TestShScalarStdoutPassesThrough(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookAfterTool, Sh: `echo hello`})
	got := runOrFail(t, c, nil)
	if got != "hello" {
		t.Errorf("got %v", got)
	}
}

func TestShNonZeroExitVetoes(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Sh: `echo boom >&2; exit 1`})
	_, err := c.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should include stderr: %v", err)
	}
}

func TestShEnvVarsUppercased(t *testing.T) {
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Sh: `echo "$TOOL_NAME"`})
	got := runOrFail(t, c, map[string]any{"tool_name": "greet"})
	if got != "greet" {
		t.Errorf("got %v", got)
	}
}

func TestShComplexEnvIsJSON(t *testing.T) {
	// Complex env values are JSON-encoded into the env var; echoing $ARGS
	// therefore emits valid JSON which the sh runner re-decodes on stdout.
	c := compileOrFail(t, bot.Hook{On: bot.HookBeforeTool, Sh: `echo "$ARGS"`})
	got := runOrFail(t, c, map[string]any{"args": map[string]any{"name": "world"}})
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T (%v)", got, got)
	}
	if m["name"] != "world" {
		t.Errorf("name = %v", m["name"])
	}
}

// --- after_tool vetoed path (regression) -------------------------------

// When before_tool vetoes, ADK still invokes after_tool with result=nil and
// inErr=<veto>. Our adapter translates nil result to an empty map so JS
// `result.x` doesn't trip on undefined. The idiomatic guard is to return
// null (pass-through) when `error` is set.
func TestAfterToolVetoPassThrough(t *testing.T) {
	c := compileOrFail(t, bot.Hook{
		On: bot.HookAfterTool,
		Js: `error ? null : ({ output: "[hooked] " + result.output })`,
	})

	// Success path: real result map, no error.
	got := runOrFail(t, c, map[string]any{
		"tool_name": "echo",
		"args":      map[string]any{"msg": "hi"},
		"result":    map[string]any{"output": "hi\n"},
		"error":     nil,
	})
	m, ok := got.(map[string]any)
	if !ok || m["output"] != "[hooked] hi\n" {
		t.Errorf("success path: got %v", got)
	}

	// Vetoed path: empty result map (as the adapter constructs) + error.
	got = runOrFail(t, c, map[string]any{
		"tool_name": "echo",
		"args":      map[string]any{"msg": "forbidden"},
		"result":    map[string]any{},
		"error":     "blocked by before_tool hook",
	})
	if got != nil {
		t.Errorf("vetoed path: expected nil pass-through, got %v", got)
	}
}

// cloneArgs must never return nil — goja binds nil as undefined and then
// `result.x` crashes.
func TestCloneArgsNeverNil(t *testing.T) {
	if cloneArgs(nil) == nil {
		t.Error("cloneArgs(nil) returned nil; adapter will pass undefined to goja")
	}
}

// --- compile errors -----------------------------------------------------

func TestCompileNoRuntime(t *testing.T) {
	_, err := Compile([]bot.Hook{{On: bot.HookBeforeTool}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCompileBadExpr(t *testing.T) {
	_, err := Compile([]bot.Hook{{On: bot.HookBeforeTool, Expr: `(((`}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCompileBadJs(t *testing.T) {
	_, err := Compile([]bot.Hook{{On: bot.HookBeforeTool, Js: `function {`}})
	if err == nil {
		t.Fatal("expected error")
	}
}
