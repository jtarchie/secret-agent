package tool

import (
	"strings"
	"testing"
)

func TestMarkdownToHTMLRendersHeadingsAndLists(t *testing.T) {
	html, err := markdownToHTML("# Hi\n\n- one\n- two\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"<h1>", "Hi</h1>", "<ul>", "<li>one</li>", "<li>two</li>"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in rendered HTML: %s", want, html)
		}
	}
}

func TestMarkdownToHTMLEscapesRawHTML(t *testing.T) {
	// goldmark's default config escapes raw HTML to prevent injection.
	html, err := markdownToHTML("hello <script>alert(1)</script>")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Errorf("raw <script> should not pass through: %s", html)
	}
}

func TestHTMLToMarkdownStructurePreserved(t *testing.T) {
	md, err := htmlToMarkdown("<h1>Hi</h1><ul><li>one</li><li>two</li></ul>")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(md, "# Hi") {
		t.Errorf("heading missing: %q", md)
	}
	if !strings.Contains(md, "- one") && !strings.Contains(md, "* one") {
		t.Errorf("first bullet missing: %q", md)
	}
	if !strings.Contains(md, "- two") && !strings.Contains(md, "* two") {
		t.Errorf("second bullet missing: %q", md)
	}
}

func TestHTMLToMarkdownHandlesEmpty(t *testing.T) {
	md, err := htmlToMarkdown("")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.TrimSpace(md) != "" {
		t.Errorf("empty HTML should map to empty markdown, got %q", md)
	}
}

func TestMarkdownRoundTripRetainsStructure(t *testing.T) {
	src := "# Title\n\nParagraph.\n\n- a\n- b\n"
	html, err := markdownToHTML(src)
	if err != nil {
		t.Fatalf("md->html: %v", err)
	}
	md, err := htmlToMarkdown(html)
	if err != nil {
		t.Fatalf("html->md: %v", err)
	}
	for _, want := range []string{"# Title", "Paragraph.", "a", "b"} {
		if !strings.Contains(md, want) {
			t.Errorf("round-trip dropped %q: %s", want, md)
		}
	}
}
