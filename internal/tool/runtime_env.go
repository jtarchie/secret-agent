package tool

import (
	"fmt"
	"strconv"

	"github.com/jtarchie/secret-agent/internal/bot"
)

// coerceRuntimeValue converts an LLM-supplied arg into a Go value typed per
// the declared param type, leniently accepting JSON-sloppy inputs (e.g. "2"
// for an integer). Shared by the expr and js runtimes so both see typed
// bindings rather than the stringified forms shell uses.
func coerceRuntimeValue(v any, t bot.ParamType) (any, error) {
	switch t { //nolint:exhaustive // ParamAttachment is resolved before reaching this helper
	case bot.ParamString, bot.ParamMarkdown:
		return coerceRuntimeString(v), nil
	case bot.ParamInteger:
		return coerceRuntimeInteger(v)
	case bot.ParamNumber:
		return coerceRuntimeNumber(v)
	case bot.ParamBoolean:
		return coerceRuntimeBoolean(v)
	}
	return nil, fmt.Errorf("unknown param type %q", t)
}

func coerceRuntimeString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func coerceRuntimeInteger(v any) (int64, error) {
	switch x := v.(type) {
	case float64:
		return int64(x), nil
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot parse %q as integer", x)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
}

func coerceRuntimeNumber(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case string:
		n, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, fmt.Errorf("cannot parse %q as number", x)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}

func coerceRuntimeBoolean(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		b, err := strconv.ParseBool(x)
		if err != nil {
			return false, fmt.Errorf("cannot parse %q as boolean", x)
		}
		return b, nil
	default:
		return false, fmt.Errorf("expected boolean, got %T", v)
	}
}
