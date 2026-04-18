package tool

import (
	"strings"
	"testing"

	"github.com/jtarchie/secret-agent/internal/chat"
)

func TestResolveAttachmentByIndex(t *testing.T) {
	atts := []chat.Attachment{
		{Path: "/a/one.txt", Filename: "one.txt"},
		{Path: "/a/two.txt", Filename: "two.txt"},
	}
	got, err := resolveAttachment("1", atts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "/a/two.txt" {
		t.Errorf("got %q, want /a/two.txt", got)
	}
}

func TestResolveAttachmentByFilename(t *testing.T) {
	atts := []chat.Attachment{
		{Path: "/a/one.txt", Filename: "one.txt"},
		{Path: "/a/two.txt", Filename: "two.txt"},
	}
	got, err := resolveAttachment("two.txt", atts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "/a/two.txt" {
		t.Errorf("got %q", got)
	}
}

func TestResolveAttachmentByBasename(t *testing.T) {
	atts := []chat.Attachment{
		{Path: "/some/dir/photo.jpg", Filename: ""},
	}
	got, err := resolveAttachment("photo.jpg", atts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "/some/dir/photo.jpg" {
		t.Errorf("got %q", got)
	}
}

func TestResolveAttachmentNoMatch(t *testing.T) {
	atts := []chat.Attachment{
		{Path: "/a/one.txt", Filename: "one.txt"},
	}
	_, err := resolveAttachment("missing.pdf", atts)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "one.txt") {
		t.Errorf("error should list what *is* available, got: %v", err)
	}
}

func TestResolveAttachmentIndexOutOfRange(t *testing.T) {
	atts := []chat.Attachment{{Path: "/a/one.txt", Filename: "one.txt"}}
	_, err := resolveAttachment("5", atts)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveAttachmentEmpty(t *testing.T) {
	_, err := resolveAttachment("0", nil)
	if err == nil {
		t.Fatal("expected error for zero attachments")
	}
}

func TestWithAttachmentsRoundtrip(t *testing.T) {
	ctx := WithAttachments(t.Context(), []chat.Attachment{{Path: "/x"}})
	out := AttachmentsFromContext(ctx)
	if len(out) != 1 || out[0].Path != "/x" {
		t.Fatalf("round-trip lost value: %+v", out)
	}
}
