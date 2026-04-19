package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// compileSh parses the shell script once and returns a runner that, on each
// invocation, injects the env map as environment variables (scalars as
// strings; complex values JSON-encoded) and interprets the script. Stdout
// is parsed as JSON if non-empty; empty stdout yields pass-through (nil).
// A non-zero exit yields an error carrying stderr.
func compileSh(script string) (func(context.Context, map[string]any) (any, error), error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "hook")
	if err != nil {
		return nil, fmt.Errorf("parse sh: %w", err)
	}

	return func(ctx context.Context, env map[string]any) (any, error) {
		vars := os.Environ()
		for k, v := range env {
			s, err := toEnvValue(v)
			if err != nil {
				return nil, fmt.Errorf("env %q: %w", k, err)
			}
			vars = append(vars, strings.ToUpper(k)+"="+s)
		}

		var stdout, stderr bytes.Buffer
		runner, err := interp.New(
			interp.Env(expand.ListEnviron(vars...)),
			interp.StdIO(nil, &stdout, &stderr),
		)
		if err != nil {
			return nil, fmt.Errorf("new shell runner: %w", err)
		}
		err = runner.Run(ctx, file)
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg != "" {
				return nil, fmt.Errorf("%w: %s", err, msg)
			}
			return nil, fmt.Errorf("shell run: %w", err)
		}

		out := strings.TrimSpace(stdout.String())
		if out == "" {
			return nil, nil //nolint:nilnil // (nil, nil) is the pass-through signal
		}
		var decoded any
		err = json.Unmarshal([]byte(out), &decoded)
		if err != nil {
			// Stdout is non-JSON; treat it as a scalar string result.
			return out, nil
		}
		return decoded, nil
	}, nil
}

func toEnvValue(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshal json: %w", err)
		}
		return string(b), nil
	}
}
