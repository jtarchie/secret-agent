package tool

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const (
	readFileDefaultLimit  = 200
	readFileMaxLimit      = 500
	readFileMaxBytes      = 8000
	readFileTruncMarker   = "\n... [truncated]"
	readFileDefaultDesc   = "Read a slice of lines from a file. Use offset and limit to avoid pulling the whole file into context."
)

// NewReadFile returns the read_file builtin tool. It reads `limit` lines from
// `file_path` starting at `offset` (zero-indexed), capped at readFileMaxBytes
// bytes of returned content. Output is the raw file slice with no line-number
// prefix — edit_file uses string match, not line numbers.
func NewReadFile(name, description string) (adktool.Tool, error) {
	if description == "" {
		description = readFileDefaultDesc
	}
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"file_path": {
				Type:        "string",
				Description: "Absolute path to the file to read.",
			},
			"offset": {
				Type:        "integer",
				Description: "Zero-indexed starting line. Default 0.",
			},
			"limit": {
				Type:        "integer",
				Description: fmt.Sprintf("Number of lines to read. Default %d, max %d.", readFileDefaultLimit, readFileMaxLimit),
			},
		},
		Required: []string{"file_path"},
	}

	tool, err := functiontool.New(
		functiontool.Config{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
		func(_ adktool.Context, args map[string]any) (shellResult, error) {
			path, err := stringArg(args, "file_path", true)
			if err != nil {
				return shellResult{}, err
			}
			offset, err := intArg(args, "offset", 0)
			if err != nil {
				return shellResult{}, err
			}
			if offset < 0 {
				return shellResult{}, fmt.Errorf("offset must be >= 0, got %d", offset)
			}
			limit, err := intArg(args, "limit", readFileDefaultLimit)
			if err != nil {
				return shellResult{}, err
			}
			if limit <= 0 {
				return shellResult{}, fmt.Errorf("limit must be > 0, got %d", limit)
			}
			if limit > readFileMaxLimit {
				limit = readFileMaxLimit
			}
			out, err := readFileSlice(path, offset, limit)
			if err != nil {
				return shellResult{}, err
			}
			return shellResult{Output: out}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("new read_file tool: %w", err)
	}
	return tool, nil
}

// readFileSlice reads up to `limit` lines starting at `offset` and caps the
// returned content at readFileMaxBytes, appending readFileTruncMarker when
// truncated.
func readFileSlice(path string, offset, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for i := 0; i < offset; i++ {
		if !scanner.Scan() {
			err := scanner.Err()
			if err != nil {
				return "", fmt.Errorf("scan: %w", err)
			}
			return "", nil
		}
	}

	var buf strings.Builder
	read := 0
	truncated := false
	for read < limit && scanner.Scan() {
		line := scanner.Text()
		if buf.Len()+len(line)+1 > readFileMaxBytes {
			truncated = true
			break
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		read++
	}
	err = scanner.Err()
	if err != nil {
		return "", fmt.Errorf("scan: %w", err)
	}
	if truncated {
		buf.WriteString(readFileTruncMarker)
	}
	return buf.String(), nil
}

// stringArg returns args[key] as a string. When required is true, an empty
// or missing value is an error.
func stringArg(args map[string]any, key string, required bool) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		if required {
			return "", fmt.Errorf("%s is required", key)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", key, v)
	}
	if required && strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}
	return s, nil
}

// intArg returns args[key] coerced to int, falling back to def when missing
// or null. Accepts float64 (JSON numbers), int variants, and strings.
func intArg(args map[string]any, key string, def int) (int, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return def, nil
	}
	switch x := v.(type) {
	case float64:
		return int(x), nil
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case string:
		var n int
		_, err := fmt.Sscanf(x, "%d", &n)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer, got %q", key, x)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("%s must be an integer, got %T", key, v)
	}
}

// boolArg returns args[key] coerced to bool, falling back to def when missing
// or null.
func boolArg(args map[string]any, key string, def bool) (bool, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return def, nil
	}
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes":
			return true, nil
		case "false", "0", "no", "":
			return false, nil
		}
		return false, fmt.Errorf("%s must be a boolean, got %q", key, x)
	default:
		return false, fmt.Errorf("%s must be a boolean, got %T", key, v)
	}
}
