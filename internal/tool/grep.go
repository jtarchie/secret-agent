package tool

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const (
	grepDefaultMax     = 30
	grepHardCap        = 50
	grepLineMaxRunes   = 200
	grepDefaultDesc    = "Search a regex in a file or directory; returns matching lines as path:line: text. Output is capped — refine the pattern if results truncate."
	grepLineTruncSfx   = "..."
)

// NewGrep returns the grep builtin tool. It walks `path` (file or directory),
// matches each line against `pattern`, and returns up to `max_results` hits.
// Each hit is "path:lineno: text"; long lines are truncated to grepLineMaxRunes.
// Hidden directories (".git" etc.) are skipped during recursion.
func NewGrep(name, description string) (adktool.Tool, error) {
	if description == "" {
		description = grepDefaultDesc
	}
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"pattern": {
				Type:        "string",
				Description: "Go RE2 regex to match against each line.",
			},
			"path": {
				Type:        "string",
				Description: "Absolute file or directory to search.",
			},
			"max_results": {
				Type:        "integer",
				Description: fmt.Sprintf("Maximum number of matches to return. Default %d, max %d.", grepDefaultMax, grepHardCap),
			},
		},
		Required: []string{"pattern", "path"},
	}

	tool, err := functiontool.New(
		functiontool.Config{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
		func(_ adktool.Context, args map[string]any) (shellResult, error) {
			pattern, err := stringArg(args, "pattern", true)
			if err != nil {
				return shellResult{}, err
			}
			path, err := stringArg(args, "path", true)
			if err != nil {
				return shellResult{}, err
			}
			max, err := intArg(args, "max_results", grepDefaultMax)
			if err != nil {
				return shellResult{}, err
			}
			if max <= 0 {
				return shellResult{}, fmt.Errorf("max_results must be > 0, got %d", max)
			}
			if max > grepHardCap {
				max = grepHardCap
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return shellResult{}, fmt.Errorf("compile regex: %w", err)
			}
			out, err := grepWalk(re, path, max)
			if err != nil {
				return shellResult{}, err
			}
			return shellResult{Output: out}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("new grep tool: %w", err)
	}
	return tool, nil
}

// grepWalk searches re across path. If path is a regular file, it scans that
// file directly; if it's a directory, it recurses (skipping hidden entries
// and unreadable files). Returns matches joined by newline; the final line
// is "[truncated at N matches]" when the cap is hit.
func grepWalk(re *regexp.Regexp, root string, max int) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}

	var lines []string
	hit := func(path string, lineno int, text string) bool {
		lines = append(lines, formatGrepLine(path, lineno, text))
		return len(lines) >= max
	}

	switch {
	case info.Mode().IsRegular():
		_, err := grepFile(re, root, hit)
		if err != nil {
			return "", err
		}
	case info.IsDir():
		err := grepDir(re, root, hit)
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("not a file or directory: %s", root)
	}

	if len(lines) >= max {
		lines = append(lines, fmt.Sprintf("[truncated at %d matches]", max))
	}
	return strings.Join(lines, "\n"), nil
}

// grepDir recursively scans every regular file under root, skipping hidden
// directories. Per-file open errors are silently ignored — the walk continues.
func grepDir(re *regexp.Regexp, root string, hit func(string, int, string) bool) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // unreadable entries are skipped silently
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		full, _ := grepFile(re, p, hit)
		if full {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}
	return nil
}

// grepFile scans path against re, calling hit for each match. Returns true
// when hit reported the cap was reached so the walker can stop.
func grepFile(re *regexp.Regexp, path string, hit func(string, int, string) bool) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, nil //nolint:nilerr // unreadable files are skipped silently
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineno := 0
	for scanner.Scan() {
		lineno++
		text := scanner.Text()
		if re.MatchString(text) {
			if hit(path, lineno, text) {
				return true, nil
			}
		}
	}
	return false, nil
}

// formatGrepLine returns "path:line: text" with text truncated at
// grepLineMaxRunes runes.
func formatGrepLine(path string, lineno int, text string) string {
	if len([]rune(text)) > grepLineMaxRunes {
		runes := []rune(text)
		text = string(runes[:grepLineMaxRunes]) + grepLineTruncSfx
	}
	return fmt.Sprintf("%s:%d: %s", path, lineno, text)
}
