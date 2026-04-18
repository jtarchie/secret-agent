package tool

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"google.golang.org/genai"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// BuildAttachedContent composes a user-role genai.Content from free-form text
// and a list of attachments. It prefixes the text with an <attachments>
// manifest so the model can reference each one by index or filename, inlines
// text/* attachments as <attachment> blocks in the text part, and emits binary
// attachments as InlineData parts. If atts is empty, returns a simple text
// content.
func BuildAttachedContent(text string, atts []chat.Attachment) (*genai.Content, error) {
	if len(atts) == 0 {
		return genai.NewContentFromText(text, genai.RoleUser), nil
	}

	type loaded struct {
		a      chat.Attachment
		data   []byte
		mime   string
		inline bool
	}
	items := make([]loaded, 0, len(atts))
	for _, a := range atts {
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", a.Path, err)
		}
		mime := a.ContentType
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		items = append(items, loaded{a: a, data: data, mime: mime, inline: IsTextMime(mime)})
	}

	var buf strings.Builder
	buf.WriteString("<attachments>\n")
	for i, it := range items {
		name := it.a.Filename
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&buf, "- index=%d filename=%q type=%q\n", i, name, it.mime)
	}
	buf.WriteString("</attachments>")

	for i, it := range items {
		if !it.inline {
			continue
		}
		name := it.a.Filename
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(&buf, "\n\n<attachment index=%d filename=%q>\n%s\n</attachment>", i, name, string(it.data))
	}

	if text != "" {
		buf.WriteString("\n\n")
		buf.WriteString(text)
	}

	parts := []*genai.Part{genai.NewPartFromText(buf.String())}
	for _, it := range items {
		if it.inline {
			continue
		}
		parts = append(parts, genai.NewPartFromBytes(it.data, it.mime))
	}
	return genai.NewContentFromParts(parts, genai.RoleUser), nil
}

// IsTextMime reports whether a MIME type should be inlined as text rather
// than shipped as binary InlineData.
func IsTextMime(mime string) bool {
	base, _, _ := strings.Cut(mime, ";")
	base = strings.TrimSpace(base)
	switch base {
	case "application/json", "application/xml", "application/yaml",
		"application/x-yaml", "application/javascript", "application/sh",
		"application/x-sh":
		return true
	}
	return strings.HasPrefix(base, "text/")
}
