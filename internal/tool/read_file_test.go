package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	err := os.WriteFile(p, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestReadFileSliceFullFile(t *testing.T) {
	p := writeTempFile(t, "a.txt", "alpha\nbeta\ngamma\n")
	got, err := readFileSlice(p, 0, 100)
	if err != nil {
		t.Fatalf("readFileSlice: %v", err)
	}
	want := "alpha\nbeta\ngamma\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadFileSliceOffsetAndLimit(t *testing.T) {
	p := writeTempFile(t, "a.txt", "1\n2\n3\n4\n5\n")
	got, err := readFileSlice(p, 1, 2)
	if err != nil {
		t.Fatalf("readFileSlice: %v", err)
	}
	want := "2\n3\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadFileSliceOffsetPastEOF(t *testing.T) {
	p := writeTempFile(t, "a.txt", "only\n")
	got, err := readFileSlice(p, 50, 10)
	if err != nil {
		t.Fatalf("readFileSlice: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestReadFileSliceTruncatesByBytes(t *testing.T) {
	// Build content much larger than readFileMaxBytes.
	big := strings.Repeat("x", 200)
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(big)
		b.WriteByte('\n')
	}
	p := writeTempFile(t, "big.txt", b.String())
	got, err := readFileSlice(p, 0, 500)
	if err != nil {
		t.Fatalf("readFileSlice: %v", err)
	}
	if !strings.HasSuffix(got, readFileTruncMarker) {
		t.Errorf("expected truncation marker, got tail %q", tail(got, 40))
	}
	if len(got) > readFileMaxBytes+len(readFileTruncMarker) {
		t.Errorf("output exceeds cap: %d bytes", len(got))
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestReadFileMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := readFileSlice(filepath.Join(dir, "nope.txt"), 0, 10)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadFileNotRegular(t *testing.T) {
	dir := t.TempDir()
	_, err := readFileSlice(dir, 0, 10)
	if err == nil {
		t.Fatal("expected error for directory path")
	}
}

func TestNewReadFileBuilds(t *testing.T) {
	tool, err := NewReadFile("read_file", "")
	if err != nil {
		t.Fatalf("NewReadFile: %v", err)
	}
	if tool == nil {
		t.Fatal("expected tool")
	}
}

func TestStringArgRequired(t *testing.T) {
	_, err := stringArg(map[string]any{}, "k", true)
	if err == nil {
		t.Fatal("expected error for missing required arg")
	}
}

func TestStringArgOptional(t *testing.T) {
	v, err := stringArg(map[string]any{}, "k", false)
	if err != nil || v != "" {
		t.Fatalf("got (%q, %v), want empty no error", v, err)
	}
}

func TestIntArgDefault(t *testing.T) {
	v, err := intArg(map[string]any{}, "k", 7)
	if err != nil || v != 7 {
		t.Fatalf("got (%d, %v), want (7, nil)", v, err)
	}
}

func TestIntArgFloat64(t *testing.T) {
	v, err := intArg(map[string]any{"k": 3.0}, "k", 0)
	if err != nil || v != 3 {
		t.Fatalf("got (%d, %v), want (3, nil)", v, err)
	}
}

func TestBoolArgDefault(t *testing.T) {
	v, err := boolArg(map[string]any{}, "k", true)
	if err != nil || v != true {
		t.Fatalf("got (%v, %v), want (true, nil)", v, err)
	}
}

func TestBoolArgString(t *testing.T) {
	v, err := boolArg(map[string]any{"k": "true"}, "k", false)
	if err != nil || v != true {
		t.Fatalf("got (%v, %v), want (true, nil)", v, err)
	}
}
