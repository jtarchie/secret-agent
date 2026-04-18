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
	switch t {
	case bot.ParamString:
		switch x := v.(type) {
		case string:
			return x, nil
		default:
			return fmt.Sprintf("%v", x), nil
		}
	case bot.ParamInteger:
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
				return nil, fmt.Errorf("cannot parse %q as integer", x)
			}
			return n, nil
		default:
			return nil, fmt.Errorf("expected integer, got %T", v)
		}
	case bot.ParamNumber:
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
				return nil, fmt.Errorf("cannot parse %q as number", x)
			}
			return n, nil
		default:
			return nil, fmt.Errorf("expected number, got %T", v)
		}
	case bot.ParamBoolean:
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			b, err := strconv.ParseBool(x)
			if err != nil {
				return nil, fmt.Errorf("cannot parse %q as boolean", x)
			}
			return b, nil
		default:
			return nil, fmt.Errorf("expected boolean, got %T", v)
		}
	}
	return nil, fmt.Errorf("unknown param type %q", t)
}
