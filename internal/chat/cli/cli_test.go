package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// nopHandler returns a handler whose channel closes immediately so the
// model's Init path is satisfied for tests.
func nopHandler(_ context.Context, _ chat.Message) <-chan chat.Chunk {
	ch := make(chan chat.Chunk)
	close(ch)
	return ch
}

// primeModelForReply puts the model into the mid-turn state the Update
// function expects when chunkMsg/streamDoneMsg arrive: a pending reply
// slot in history with replyIdx pointing at it.
func primeModelForReply(t *testing.T) *model {
	t.Helper()
	return primeModelWithPrefix(t, "")
}

func primeModelWithPrefix(t *testing.T, prefix string) *model {
	t.Helper()
	m := newModel(context.Background(), "bot", prefix, nopHandler, false)
	m.history = append(m.history, "you: hi", m.thinkingLine())
	m.replyIdx = len(m.history) - 1
	m.waiting = true
	return m
}

func TestChunkErrorSurvivesStreamDone(t *testing.T) {
	m := primeModelForReply(t)

	m.Update(chunkMsg(chat.Chunk{Err: errors.New("mcp connect: boom")}))
	m.Update(streamDoneMsg{})

	joined := strings.Join(m.history, "\n")
	if !strings.Contains(joined, "error") || !strings.Contains(joined, "mcp connect: boom") {
		t.Fatalf("error message was stripped after stream close; history=%q", joined)
	}
}

func TestEmptyReplyLineIsStrippedOnStreamDone(t *testing.T) {
	m := primeModelForReply(t)
	thinking := m.thinkingLine()

	m.Update(streamDoneMsg{})

	for _, line := range m.history {
		if line == thinking {
			t.Fatalf("thinking placeholder should have been removed; history=%q", m.history)
		}
	}
}

func TestReplyTextSurvivesStreamDone(t *testing.T) {
	m := primeModelForReply(t)

	m.Update(chunkMsg(chat.Chunk{Delta: "hello"}))
	m.Update(streamDoneMsg{})

	joined := strings.Join(m.history, "\n")
	if !strings.Contains(joined, "hello") {
		t.Fatalf("reply text was stripped; history=%q", joined)
	}
}

func TestMessagePrefixAppearsOnceAtStartOfReply(t *testing.T) {
	m := primeModelWithPrefix(t, "[bot] ")

	m.Update(chunkMsg(chat.Chunk{Delta: "hel"}))
	m.Update(chunkMsg(chat.Chunk{Delta: "lo"}))
	m.Update(streamDoneMsg{})

	if m.lastReply != "[bot] hello" {
		t.Fatalf("want prefix prepended once; got %q", m.lastReply)
	}
}

func TestMessagePrefixSkippedWhenReplyIsEmpty(t *testing.T) {
	m := primeModelWithPrefix(t, "[bot] ")

	m.Update(streamDoneMsg{})

	for _, line := range m.history {
		if strings.Contains(line, "[bot]") {
			t.Fatalf("prefix leaked into UI for empty reply; history=%q", m.history)
		}
	}
}
