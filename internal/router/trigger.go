package router

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// peerBufferCap bounds how many un-triggered messages we remember per conv
// before the oldest entry is dropped. Flushed on every trigger or restart.
const peerBufferCap = 10

// triggerMatcher returns true if a message text contains any configured
// trigger word at a word boundary, case-insensitively.
type triggerMatcher struct {
	res []*regexp.Regexp
}

func newTriggerMatcher(words []string) (*triggerMatcher, error) {
	if len(words) == 0 {
		return &triggerMatcher{}, nil
	}
	out := make([]*regexp.Regexp, 0, len(words))
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		// (?:^|\W) and (?:\W|$) are byte-level boundaries that fire around
		// leading punctuation like '@' which Go's \b does not.
		pat := `(?i)(?:^|\W)` + regexp.QuoteMeta(w) + `(?:\W|$)`
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("compile trigger %q: %w", w, err)
		}
		out = append(out, re)
	}
	return &triggerMatcher{res: out}, nil
}

func (m *triggerMatcher) Matches(text string) bool {
	for _, re := range m.res {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// peerBuffer is a bounded FIFO of un-triggered message texts for a single
// conversation. Drain returns a copy and clears the buffer.
type peerBuffer struct {
	mu   sync.Mutex
	msgs []string
}

func (b *peerBuffer) Append(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) >= peerBufferCap {
		b.msgs = b.msgs[1:]
	}
	b.msgs = append(b.msgs, text)
}

func (b *peerBuffer) Drain() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) == 0 {
		return nil
	}
	out := make([]string, len(b.msgs))
	copy(out, b.msgs)
	b.msgs = b.msgs[:0]
	return out
}

// wrapWithPrior combines buffered prior messages with the active turn in a
// single XML-ish block the model can distinguish from the current message.
// Called with prior already non-empty.
func wrapWithPrior(prior []string, current string) string {
	var sb strings.Builder
	sb.WriteString("<previous_messages>\n")
	for _, p := range prior {
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	sb.WriteString("</previous_messages>\n\n")
	sb.WriteString(current)
	return sb.String()
}
