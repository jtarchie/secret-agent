package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAttachmentsNoToken(t *testing.T) {
	text := "just a plain message"
	got, atts, err := parseAttachments(text)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != text {
		t.Errorf("text = %q, want unchanged", got)
	}
	if atts != nil {
		t.Errorf("atts = %v, want nil", atts)
	}
}

func TestParseAttachmentsUnquoted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	text := "summarize #file:" + path + " please"
	got, atts, err := parseAttachments(text)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "summarize [attached: hello.txt] please"
	if got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if len(atts) != 1 {
		t.Fatalf("atts len = %d", len(atts))
	}
	if atts[0].Path != path {
		t.Errorf("path = %q, want %q", atts[0].Path, path)
	}
	if atts[0].Filename != "hello.txt" {
		t.Errorf("filename = %q", atts[0].Filename)
	}
}

func TestParseAttachmentsQuotedWithSpaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my photo.jpg")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	text := `look #file:"` + path + `" please`
	got, atts, err := parseAttachments(text)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "[attached: my photo.jpg]") {
		t.Errorf("text = %q missing attachment placeholder", got)
	}
	if len(atts) != 1 || atts[0].Path != path {
		t.Errorf("atts = %+v", atts)
	}
}

func TestParseAttachmentsMissingFile(t *testing.T) {
	text := "check #file:/definitely/does/not/exist.png"
	_, _, err := parseAttachments(text)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseAttachmentsDirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	text := "check #file:" + dir
	_, _, err := parseAttachments(text)
	if err == nil {
		t.Fatal("expected error for directory")
	}
}

func TestParseAttachmentsMultiple(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.txt")
	p2 := filepath.Join(dir, "b.txt")
	for _, p := range []string{p1, p2} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	text := "diff #file:" + p1 + " and #file:" + p2
	got, atts, err := parseAttachments(text)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(atts) != 2 {
		t.Fatalf("atts len = %d", len(atts))
	}
	if !strings.Contains(got, "[attached: a.txt]") || !strings.Contains(got, "[attached: b.txt]") {
		t.Errorf("text missing placeholders: %q", got)
	}
}
