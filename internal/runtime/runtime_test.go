package runtime

import (
	"os"
	"path/filepath"
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
	if c.Parts[0].Text != "what is this" {
		t.Errorf("text part = %q", c.Parts[0].Text)
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
	if len(c.Parts) != 1 {
		t.Fatalf("parts = %d, want 1 (empty text omitted)", len(c.Parts))
	}
	if c.Parts[0].InlineData.MIMEType != "application/pdf" {
		t.Errorf("mime = %q", c.Parts[0].InlineData.MIMEType)
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
