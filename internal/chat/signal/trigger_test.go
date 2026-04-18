package signal

import (
	"strings"
	"testing"
)

func TestTriggerMatcher_EmptyReturnsNil(t *testing.T) {
	m, err := newTriggerMatcher(nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil matcher for empty list")
	}

	m, err = newTriggerMatcher([]string{"", "   "})
	if err != nil {
		t.Fatalf("unexpected err for whitespace-only list: %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil matcher when all entries are whitespace")
	}
}

func TestTriggerMatcher_WordBoundaryCaseInsensitive(t *testing.T) {
	m, err := newTriggerMatcher([]string{"@bot", "@willow"})
	if err != nil {
		t.Fatalf("build matcher: %v", err)
	}

	cases := []struct {
		text string
		want bool
	}{
		{"@bot", true},
		{"@Bot what's up", true},
		{"hey @BOT!", true},
		{"@bot,", true},
		{"please @willow thoughts?", true},
		{"   @bot   ", true},
		{"yo @bots", false},
		{"@bottom shelf", false},
		{"robot", false},
		{"nothing to see", false},
		{"", false},
	}
	for _, tc := range cases {
		got := m.Matches(tc.text)
		if got != tc.want {
			t.Errorf("Matches(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestTriggerMatcher_SpecialCharsEscaped(t *testing.T) {
	// regex metacharacters in the trigger must be treated literally.
	m, err := newTriggerMatcher([]string{"a.b+c"})
	if err != nil {
		t.Fatalf("build matcher: %v", err)
	}
	if !m.Matches("hey a.b+c please") {
		t.Error("expected literal match for a.b+c")
	}
	if m.Matches("hey aXb+c please") {
		t.Error("'.' should not match arbitrary char")
	}
}

func TestPeerBuffer_AppendDrain(t *testing.T) {
	var b peerBuffer
	if got := b.Drain(); got != nil {
		t.Fatalf("empty Drain = %v, want nil", got)
	}

	b.Append("one")
	b.Append("two")
	got := b.Drain()
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("Drain = %v, want [one two]", got)
	}

	if got := b.Drain(); got != nil {
		t.Fatalf("Drain after clear = %v, want nil", got)
	}
}

func TestPeerBuffer_Overflow(t *testing.T) {
	var b peerBuffer
	for i := 0; i < peerBufferCap+3; i++ {
		b.Append(stringOf('a' + byte(i)))
	}
	got := b.Drain()
	if len(got) != peerBufferCap {
		t.Fatalf("len after overflow = %d, want %d", len(got), peerBufferCap)
	}
	// Oldest 3 should have been evicted: first remaining is index 3.
	want := stringOf('a' + 3)
	if got[0] != want {
		t.Errorf("oldest retained = %q, want %q", got[0], want)
	}
}

func TestWrapWithPrior(t *testing.T) {
	out := wrapWithPrior([]string{"hey", "so about that thing"}, "@bot what do you think?")
	if !strings.Contains(out, "<previous_messages>") ||
		!strings.Contains(out, "</previous_messages>") {
		t.Fatalf("missing envelope tags: %q", out)
	}
	if !strings.Contains(out, "hey\n") || !strings.Contains(out, "so about that thing\n") {
		t.Errorf("priors not joined with newlines: %q", out)
	}
	if !strings.HasSuffix(out, "@bot what do you think?") {
		t.Errorf("current message not at end: %q", out)
	}
}

func stringOf(b byte) string { return string([]byte{b}) }
