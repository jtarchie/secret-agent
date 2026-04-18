package tool

import (
	"testing"

	"github.com/jtarchie/secret-agent/internal/bot"
)

func TestCoerceRuntimeValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		typ  bot.ParamType
		want any
	}{
		{"string passthrough", "hi", bot.ParamString, "hi"},
		{"integer from float", float64(2), bot.ParamInteger, int64(2)},
		{"integer from string", "7", bot.ParamInteger, int64(7)},
		{"number from int", 3, bot.ParamNumber, float64(3)},
		{"number from string", "1.5", bot.ParamNumber, 1.5},
		{"bool passthrough", true, bot.ParamBoolean, true},
		{"bool from string", "false", bot.ParamBoolean, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := coerceRuntimeValue(c.in, c.typ)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v (%T), want %v (%T)", got, got, c.want, c.want)
			}
		})
	}
}

func TestCoerceRuntimeValueRejects(t *testing.T) {
	if _, err := coerceRuntimeValue("not-a-number", bot.ParamInteger); err == nil {
		t.Error("expected error for garbage integer")
	}
	if _, err := coerceRuntimeValue(struct{}{}, bot.ParamBoolean); err == nil {
		t.Error("expected error for non-boolean type")
	}
}
