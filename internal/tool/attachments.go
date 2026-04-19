package tool

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jtarchie/secret-agent/internal/chat"
)

type attachmentsKey struct{}

// WithAttachments returns a context carrying the turn's attachments so shell
// tools with `type: attachment` params can resolve model-supplied references.
func WithAttachments(ctx context.Context, atts []chat.Attachment) context.Context {
	if len(atts) == 0 {
		return ctx
	}
	return context.WithValue(ctx, attachmentsKey{}, atts)
}

// AttachmentsFromContext returns the turn's attachments, or nil.
func AttachmentsFromContext(ctx context.Context) []chat.Attachment {
	v, _ := ctx.Value(attachmentsKey{}).([]chat.Attachment)
	return v
}

// resolveAttachment maps a model-supplied reference (numeric index or
// filename) to the local file path of the matching attachment. Returns a
// descriptive error listing what *is* available so the model can retry.
func resolveAttachment(value any, atts []chat.Attachment) (string, error) {
	if len(atts) == 0 {
		return "", errors.New("no attachments in this turn")
	}
	s, ok := value.(string)
	if !ok {
		s = fmt.Sprintf("%v", value)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty attachment reference")
	}

	n, err := strconv.Atoi(s)
	if err == nil {
		if n < 0 || n >= len(atts) {
			return "", fmt.Errorf("attachment index %d out of range (have %d)", n, len(atts))
		}
		return atts[n].Path, nil
	}

	for _, a := range atts {
		if a.Filename == s {
			return a.Path, nil
		}
	}
	for _, a := range atts {
		if filepath.Base(a.Path) == s {
			return a.Path, nil
		}
	}

	avail := make([]string, len(atts))
	for i, a := range atts {
		name := a.Filename
		if name == "" {
			name = filepath.Base(a.Path)
		}
		avail[i] = fmt.Sprintf("%d=%s", i, name)
	}
	return "", fmt.Errorf("no attachment matches %q; available: %s", s, strings.Join(avail, ", "))
}
