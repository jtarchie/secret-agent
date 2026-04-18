package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jtarchie/secret-agent/internal/chat"
)

func TestBuildUserContentTextOnly(t *testing.T) {
	c, err := buildUserContent(chat.Message{Text: "hi"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(c.Parts) != 1 || c.Parts[0].Text != "hi" {
		t.Fatalf("parts = %+v", c.Parts)
	}
}

// A minimal PNG signature — enough for http.DetectContentType to return image/png.
var pngBytes = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0, 0, 0, 0, 0}

func TestBuildUserContentSniffsMime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "img.bin")
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := buildUserContent(chat.Message{
		Text:        "what is this",
		Attachments: []chat.Attachment{{Path: path, Filename: "img.bin"}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(c.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(c.Parts))
	}
	if !strings.Contains(c.Parts[0].Text, "what is this") {
		t.Errorf("text part missing user text: %q", c.Parts[0].Text)
	}
	if c.Parts[1].InlineData == nil {
		t.Fatal("expected InlineData on part 1")
	}
	if c.Parts[1].InlineData.MIMEType != "image/png" {
		t.Errorf("mime = %q, want image/png", c.Parts[1].InlineData.MIMEType)
	}
}

func TestBuildUserContentUsesProvidedMime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("not really a pdf"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := buildUserContent(chat.Message{
		Attachments: []chat.Attachment{{
			Path:        path,
			Filename:    "doc.pdf",
			ContentType: "application/pdf",
		}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(c.Parts) != 2 {
		t.Fatalf("parts = %d, want 2 (manifest + inline)", len(c.Parts))
	}
	if !strings.Contains(c.Parts[0].Text, `type="application/pdf"`) {
		t.Errorf("manifest missing type: %q", c.Parts[0].Text)
	}
	if c.Parts[1].InlineData.MIMEType != "application/pdf" {
		t.Errorf("mime = %q", c.Parts[1].InlineData.MIMEType)
	}
}

func TestBuildUserContentManifest(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "photo.bin")
	if err := os.WriteFile(p1, pngBytes, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	p2 := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(p2, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := buildUserContent(chat.Message{
		Text: "summarize these",
		Attachments: []chat.Attachment{
			{Path: p1, Filename: "photo.jpg"},
			{Path: p2, Filename: "doc.pdf", ContentType: "application/pdf"},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(c.Parts) != 3 {
		t.Fatalf("parts = %d, want 3 (text + 2 inline)", len(c.Parts))
	}
	text := c.Parts[0].Text
	for _, want := range []string{
		"<attachments>",
		`index=0 filename="photo.jpg" type="image/png"`,
		`index=1 filename="doc.pdf" type="application/pdf"`,
		"</attachments>",
		"summarize these",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q, got:\n%s", want, text)
		}
	}
	// The manifest must come before the user text so the model frames the
	// attachments before reading the ask.
	if strings.Index(text, "<attachments>") > strings.Index(text, "summarize these") {
		t.Error("manifest should precede user text")
	}
}

func TestBuildUserContentInlinesTextAttachments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	body := "# Hello\n\nthis is the body"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := buildUserContent(chat.Message{
		Text: "summarize",
		Attachments: []chat.Attachment{
			{Path: path, Filename: "notes.md", ContentType: "text/markdown"},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Only one part: text. No InlineData binary part for a text file.
	if len(c.Parts) != 1 {
		t.Fatalf("parts = %d, want 1 (text only for text/* attachments)", len(c.Parts))
	}
	text := c.Parts[0].Text
	for _, want := range []string{
		`<attachment index=0 filename="notes.md">`,
		body,
		"</attachment>",
		"summarize",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q, got:\n%s", want, text)
		}
	}
}

func TestIsTextMime(t *testing.T) {
	cases := map[string]bool{
		"text/plain":             true,
		"text/markdown":          true,
		"text/plain; charset=utf-8": true,
		"application/json":       true,
		"application/xml":        true,
		"image/png":              false,
		"application/pdf":        false,
		"application/octet-stream": false,
	}
	for in, want := range cases {
		if got := isTextMime(in); got != want {
			t.Errorf("isTextMime(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBuildUserContentMissingFile(t *testing.T) {
	_, err := buildUserContent(chat.Message{
		Attachments: []chat.Attachment{{Path: "/does/not/exist"}},
	})
	if err == nil {
		t.Fatal("expected error for missing attachment")
	}
}
