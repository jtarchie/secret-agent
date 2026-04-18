package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// fileRe matches #file:<path>. Path is either "quoted" (allowing spaces) or
// a run of non-space characters.
var fileRe = regexp.MustCompile(`#file:(?:"([^"]+)"|(\S+))`)

// parseAttachments scans text for #file:<path> tokens. For each match, it
// resolves the path to an absolute location and stats it. On success, returns
// the text with each token replaced by "[attached: <basename>]" along with one
// chat.Attachment per match. If any referenced file is missing or is a
// directory, returns an error identifying the first bad path so the caller can
// surface it before sending the turn.
func parseAttachments(text string) (string, []chat.Attachment, error) {
	matches := fileRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil, nil
	}
	var (
		out  strings.Builder
		atts []chat.Attachment
		last int
	)
	for _, m := range matches {
		var path string
		switch {
		case m[2] >= 0:
			path = text[m[2]:m[3]]
		case m[4] >= 0:
			path = text[m[4]:m[5]]
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", nil, fmt.Errorf("resolve %s: %w", path, err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return "", nil, fmt.Errorf("stat %s: %w", path, err)
		}
		if st.IsDir() {
			return "", nil, fmt.Errorf("%s is a directory", path)
		}
		base := filepath.Base(abs)
		out.WriteString(text[last:m[0]])
		out.WriteString("[attached: ")
		out.WriteString(base)
		out.WriteString("]")
		atts = append(atts, chat.Attachment{
			Path:     abs,
			Filename: base,
		})
		last = m[1]
	}
	out.WriteString(text[last:])
	return out.String(), atts, nil
}
