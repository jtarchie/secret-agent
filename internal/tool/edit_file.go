package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const editFileDefaultDesc = "Replace old_string with new_string in a file. Without replace_all, old_string must occur exactly once — include surrounding context to make it unique."

// editFileResult is the JSON shape returned to the LLM. Reporting only the
// count keeps the response tiny — we deliberately do not echo the file.
type editFileResult struct {
	Replacements int    `json:"replacements"`
	Path         string `json:"path"`
}

// NewEditFile returns the edit_file builtin tool. Behavior:
//   - Reads file_path from disk.
//   - Errors if old_string == new_string.
//   - With replace_all=false (default): errors unless old_string occurs
//     exactly once.
//   - With replace_all=true: replaces every occurrence; errors on zero.
//   - Writes atomically via temp-file-and-rename in the same directory.
func NewEditFile(name, description string) (adktool.Tool, error) {
	if description == "" {
		description = editFileDefaultDesc
	}
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"file_path": {
				Type:        "string",
				Description: "Absolute path to the file to edit.",
			},
			"old_string": {
				Type:        "string",
				Description: "Exact text to replace. Must occur exactly once unless replace_all is true.",
			},
			"new_string": {
				Type:        "string",
				Description: "Replacement text.",
			},
			"replace_all": {
				Type:        "boolean",
				Description: "When true, replace every occurrence. Default false.",
			},
		},
		Required: []string{"file_path", "old_string", "new_string"},
	}

	tool, err := functiontool.New(
		functiontool.Config{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
		func(_ adktool.Context, args map[string]any) (editFileResult, error) {
			path, err := stringArg(args, "file_path", true)
			if err != nil {
				return editFileResult{}, err
			}
			oldStr, err := stringArg(args, "old_string", true)
			if err != nil {
				return editFileResult{}, err
			}
			// new_string is required by schema but may be empty (deletion).
			newStr, _ := args["new_string"].(string)
			replaceAll, err := boolArg(args, "replace_all", false)
			if err != nil {
				return editFileResult{}, err
			}
			n, err := editFileApply(path, oldStr, newStr, replaceAll)
			if err != nil {
				return editFileResult{}, err
			}
			return editFileResult{Replacements: n, Path: path}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("new edit_file tool: %w", err)
	}
	return tool, nil
}

// editFileApply reads path, validates the replacement, and writes the result
// atomically. Returns the number of replacements made.
func editFileApply(path, oldStr, newStr string, replaceAll bool) (int, error) {
	if oldStr == newStr {
		return 0, errors.New("old_string and new_string must differ")
	}
	if oldStr == "" {
		return 0, errors.New("old_string must not be empty")
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("not a regular file: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}
	orig := string(data)

	count := strings.Count(orig, oldStr)
	switch {
	case count == 0:
		return 0, fmt.Errorf("old_string not found in %s", path)
	case count > 1 && !replaceAll:
		return 0, fmt.Errorf("old_string occurs %d times in %s; pass replace_all=true or include more context to make it unique", count, path)
	}

	var updated string
	var n int
	if replaceAll {
		updated = strings.ReplaceAll(orig, oldStr, newStr)
		n = count
	} else {
		updated = strings.Replace(orig, oldStr, newStr, 1)
		n = 1
	}

	err = atomicWrite(path, []byte(updated), info.Mode().Perm())
	if err != nil {
		return 0, err
	}
	return n, nil
}

// atomicWrite writes data to a temp file in the same directory as path and
// renames it over path. The rename is atomic on POSIX filesystems.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".edit-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	_, err = tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	err = tmp.Chmod(mode)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	err = tmp.Close()
	if err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	err = os.Rename(tmpPath, path)
	if err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	cleanup = false
	return nil
}
