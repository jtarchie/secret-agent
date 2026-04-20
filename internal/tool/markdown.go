package tool

import (
	"bytes"
	"fmt"

	"github.com/yuin/goldmark"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// markdownToHTML renders a Markdown string as HTML using goldmark's default
// CommonMark configuration. Used for `markdown` param types so tools receive
// a pre-rendered `<NAME>_HTML` env var alongside the raw markdown.
func markdownToHTML(src string) (string, error) {
	var buf bytes.Buffer
	err := goldmark.New().Convert([]byte(src), &buf)
	if err != nil {
		return "", fmt.Errorf("markdown->html: %w", err)
	}
	return buf.String(), nil
}

// htmlToMarkdown converts an HTML fragment back to Markdown. Used for tools
// that declare `returns: markdown` so HTML-emitting sh: scripts can produce
// clean markdown output for the LLM without embedding a converter.
func htmlToMarkdown(src string) (string, error) {
	md, err := htmltomarkdown.ConvertString(src)
	if err != nil {
		return "", fmt.Errorf("html->markdown: %w", err)
	}
	return md, nil
}
