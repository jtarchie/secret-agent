package tool

import (
	"testing"

	"github.com/expr-lang/expr"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
)

func TestNewExprCompileError(t *testing.T) {
	_, err := NewExpr("bad", "d", "a +", map[string]bot.Param{})
	if err == nil {
		t.Fatal("expected compile error for invalid expression")
	}
}

func TestNewExprValid(t *testing.T) {
	tool, err := NewExpr("add", "adds", "a + b", map[string]bot.Param{
		"a": {Type: bot.ParamNumber, Required: true},
		"b": {Type: bot.ParamNumber, Required: true},
	})
	if err != nil {
		t.Fatalf("NewExpr: %v", err)
	}
	if tool == nil {
		t.Fatal("expected tool")
	}
}

func TestRunExprAddition(t *testing.T) {
	program, err := expr.Compile("a + b", expr.AllowUndefinedVariables())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := runExpr(program, map[string]any{"a": 2, "b": 3})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != 5 {
		t.Errorf("got %v, want 5", out)
	}
}

func TestBuildRuntimeEnvAppliesDefault(t *testing.T) {
	env, err := buildRuntimeEnv("t",
		map[string]bot.Param{
			"x": {Type: bot.ParamInteger, Default: int64(42)},
		},
		map[string]any{},
		nil,
	)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if env["x"] != int64(42) {
		t.Errorf("want default 42, got %v", env["x"])
	}
}

func TestBuildRuntimeEnvAttachmentAsPath(t *testing.T) {
	env, err := buildRuntimeEnv("t",
		map[string]bot.Param{"f": {Type: bot.ParamAttachment, Required: true}},
		map[string]any{"f": "0"},
		[]chat.Attachment{{Path: "/tmp/a.txt", Filename: "a.txt"}},
	)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if env["f"] != "/tmp/a.txt" {
		t.Errorf("want /tmp/a.txt, got %v", env["f"])
	}
}
