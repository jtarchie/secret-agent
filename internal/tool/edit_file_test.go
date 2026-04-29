package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFileSingleReplaceSucceeds(t *testing.T) {
	p := writeTempFile(t, "f.txt", "alpha foo gamma\n")
	n, err := editFileApply(p, "foo", "bar", false)
	if err != nil {
		t.Fatalf("editFileApply: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d replacements, want 1", n)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "alpha bar gamma\n" {
		t.Errorf("file content unexpected: %q", got)
	}
}

func TestEditFileMultipleWithoutReplaceAllErrors(t *testing.T) {
	p := writeTempFile(t, "f.txt", "foo foo foo\n")
	_, err := editFileApply(p, "foo", "bar", false)
	if err == nil {
		t.Fatal("expected error for multiple matches without replace_all")
	}
	if !strings.Contains(err.Error(), "occurs 3 times") {
		t.Errorf("unexpected error: %v", err)
	}
	// File should be unchanged.
	got, _ := os.ReadFile(p)
	if string(got) != "foo foo foo\n" {
		t.Errorf("file should be untouched, got %q", got)
	}
}

func TestEditFileReplaceAllReplacesEvery(t *testing.T) {
	p := writeTempFile(t, "f.txt", "foo foo foo\n")
	n, err := editFileApply(p, "foo", "bar", true)
	if err != nil {
		t.Fatalf("editFileApply: %v", err)
	}
	if n != 3 {
		t.Errorf("got %d replacements, want 3", n)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "bar bar bar\n" {
		t.Errorf("file content unexpected: %q", got)
	}
}

func TestEditFileZeroOccurrencesErrors(t *testing.T) {
	p := writeTempFile(t, "f.txt", "alpha gamma\n")
	_, err := editFileApply(p, "foo", "bar", false)
	if err == nil {
		t.Fatal("expected error when old_string is absent")
	}
}

func TestEditFileIdenticalStringsErrors(t *testing.T) {
	p := writeTempFile(t, "f.txt", "foo\n")
	_, err := editFileApply(p, "foo", "foo", false)
	if err == nil {
		t.Fatal("expected error when old == new")
	}
}

func TestEditFileEmptyOldStringErrors(t *testing.T) {
	p := writeTempFile(t, "f.txt", "anything\n")
	_, err := editFileApply(p, "", "x", false)
	if err == nil {
		t.Fatal("expected error for empty old_string")
	}
}

func TestEditFileMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := editFileApply(filepath.Join(dir, "nope.txt"), "x", "y", false)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestEditFileNotRegularErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := editFileApply(dir, "x", "y", false)
	if err == nil {
		t.Fatal("expected error for non-regular path")
	}
}

func TestEditFilePreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	err := os.WriteFile(p, []byte("foo\n"), 0o640)
	if err != nil {
		t.Fatal(err)
	}
	_, err = editFileApply(p, "foo", "bar", false)
	if err != nil {
		t.Fatalf("editFileApply: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("perm changed: got %o, want 0640", info.Mode().Perm())
	}
}

func TestEditFileLeavesNoTempOnError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	err := os.WriteFile(p, []byte("foo foo\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = editFileApply(p, "foo", "bar", false)
	if err == nil {
		t.Fatal("expected error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".edit-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

func TestNewEditFileBuilds(t *testing.T) {
	tool, err := NewEditFile("edit_file", "")
	if err != nil {
		t.Fatalf("NewEditFile: %v", err)
	}
	if tool == nil {
		t.Fatal("expected tool")
	}
}
