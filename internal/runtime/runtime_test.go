package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adkmodel "google.golang.org/adk/model"

	"github.com/jtarchie/secret-agent/internal/bot"
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
	err := os.WriteFile(path, pngBytes, 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := buildUserContent(chat.Message{
		Text:        "what is this",
		Attachments: []chat.Attachment{{Path: path, Filename: "img.bin"}},
	})
	if err != nil {
		t.Fatalf("build err: %v", err)
	}
	if len(c.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(c.Parts))
	}
	if !strings.Contains(c.Parts[0].Text, "what is this") {
		t.Errorf("text part missing user text: %q", c.Parts[0].Text)
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
	err := os.WriteFile(path, []byte("not really a pdf"), 0o600)
	if err != nil {
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
		t.Fatalf("build err: %v", err)
	}
	if len(c.Parts) != 2 {
		t.Fatalf("parts = %d, want 2 (manifest + inline)", len(c.Parts))
	}
	if !strings.Contains(c.Parts[0].Text, `type="application/pdf"`) {
		t.Errorf("manifest missing type: %q", c.Parts[0].Text)
	}
	if c.Parts[1].InlineData.MIMEType != "application/pdf" {
		t.Errorf("mime = %q", c.Parts[1].InlineData.MIMEType)
	}
}

func TestBuildUserContentManifest(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "photo.bin")
	err := os.WriteFile(p1, pngBytes, 0o600)
	if err != nil {
		t.Fatalf("write p1: %v", err)
	}
	p2 := filepath.Join(dir, "doc.pdf")
	err = os.WriteFile(p2, []byte("x"), 0o600)
	if err != nil {
		t.Fatalf("write p2: %v", err)
	}

	c, err := buildUserContent(chat.Message{
		Text: "summarize these",
		Attachments: []chat.Attachment{
			{Path: p1, Filename: "photo.jpg"},
			{Path: p2, Filename: "doc.pdf", ContentType: "application/pdf"},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(c.Parts) != 3 {
		t.Fatalf("parts = %d, want 3 (text + 2 inline)", len(c.Parts))
	}
	text := c.Parts[0].Text
	for _, want := range []string{
		"<attachments>",
		`index=0 filename="photo.jpg" type="image/png"`,
		`index=1 filename="doc.pdf" type="application/pdf"`,
		"</attachments>",
		"summarize these",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q, got:\n%s", want, text)
		}
	}
	// The manifest must come before the user text so the model frames the
	// attachments before reading the ask.
	if strings.Index(text, "<attachments>") > strings.Index(text, "summarize these") {
		t.Error("manifest should precede user text")
	}
}

func TestBuildUserContentInlinesTextAttachments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	body := "# Hello\n\nthis is the body"
	err := os.WriteFile(path, []byte(body), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	c, err := buildUserContent(chat.Message{
		Text: "summarize",
		Attachments: []chat.Attachment{
			{Path: path, Filename: "notes.md", ContentType: "text/markdown"},
		},
	})
	if err != nil {
		t.Fatalf("build err: %v", err)
	}
	// Only one part: text. No InlineData binary part for a text file.
	if len(c.Parts) != 1 {
		t.Fatalf("parts = %d, want 1 (text only for text/* attachments)", len(c.Parts))
	}
	text := c.Parts[0].Text
	for _, want := range []string{
		`<attachment index=0 filename="notes.md">`,
		body,
		"</attachment>",
		"summarize",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q, got:\n%s", want, text)
		}
	}
}

func TestIsTextMime(t *testing.T) {
	cases := map[string]bool{
		"text/plain":                true,
		"text/markdown":             true,
		"text/plain; charset=utf-8": true,
		"application/json":          true,
		"application/xml":           true,
		"image/png":                 false,
		"application/pdf":           false,
		"application/octet-stream":  false,
	}
	for in, want := range cases {
		if got := isTextMime(in); got != want {
			t.Errorf("isTextMime(%q) = %v, want %v", in, got, want)
		}
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

func TestNewStatelessFlag(t *testing.T) {
	ctx := context.Background()

	b := writeBot(t, `
name: b
system: s
`)
	rt, err := New(ctx, b, stubLLM{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if rt.stateless {
		t.Error("default bot should not be stateless")
	}

	bStateless := writeBot(t, `
name: b
system: s
permissions:
  memory: none
`)
	rtStateless, err := New(ctx, bStateless, stubLLM{})
	if err != nil {
		t.Fatalf("New stateless: %v", err)
	}
	if !rtStateless.stateless {
		t.Error("memory: none should set stateless = true")
	}
}

func TestStatelessHandlerCreatesFreshSessions(t *testing.T) {
	ctx := context.Background()
	b := writeBot(t, `
name: b
system: s
permissions:
  memory: none
`)
	rt, err := New(ctx, b, stubLLM{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := rt.HandlerFor("conv-x")
	for i := 0; i < 3; i++ {
		ch := h(ctx, chat.Message{Text: "ping"})
		for range ch { // stubLLM emits nothing; drain to completion
		}
	}

	rt.mu.Lock()
	got := len(rt.known)
	rt.mu.Unlock()
	if got != 3 {
		t.Errorf("stateless should create a session per turn; len(known) = %d, want 3", got)
	}
	if seq := rt.turnSeq.Load(); seq != 3 {
		t.Errorf("turnSeq = %d, want 3", seq)
	}
}

func TestFullMemoryReusesSession(t *testing.T) {
	ctx := context.Background()
	b := writeBot(t, `
name: b
system: s
`)
	rt, err := New(ctx, b, stubLLM{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h := rt.HandlerFor("conv-y")
	for i := 0; i < 3; i++ {
		ch := h(ctx, chat.Message{Text: "ping"})
		for range ch {
		}
	}

	rt.mu.Lock()
	got := len(rt.known)
	rt.mu.Unlock()
	if got != 1 {
		t.Errorf("default mode should reuse one session; len(known) = %d, want 1", got)
	}
}

var _ = bot.MemoryFull // keep bot import used if helpers change

func TestNewWithMCPStdio(t *testing.T) {
	ctx := context.Background()
	b := writeBot(t, `
name: b
system: s
mcp:
  - name: fake
    command: echo
    args: ["hello"]
`)
	rt, err := New(ctx, b, stubLLM{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if rt == nil {
		t.Fatal("got nil runtime")
	}
}

func TestPreflightMCPNoServers(t *testing.T) {
	ctx := context.Background()
	b := writeBot(t, `
name: b
system: s
`)
	rt, err := New(ctx, b, stubLLM{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = rt.PreflightMCP(ctx, 1*time.Second)
	if err != nil {
		t.Fatalf("PreflightMCP with no servers should be a no-op, got: %v", err)
	}
}

func TestPreflightMCPTimesOutOnDeadServer(t *testing.T) {
	ctx := context.Background()
	b := writeBot(t, `
name: b
system: s
mcp:
  - name: dead
    url: http://127.0.0.1:1
`)
	rt, err := New(ctx, b, stubLLM{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	err = rt.PreflightMCP(ctx, 250*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected preflight error against dead server")
	}
	if !strings.Contains(err.Error(), "dead") {
		t.Errorf("error should name the failing server; got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("preflight took too long (%s); timeout should have fired", elapsed)
	}
}

func TestNewWithMCPHTTP(t *testing.T) {
	ctx := context.Background()
	b := writeBot(t, `
name: b
system: s
mcp:
  - name: remote
    url: https://example.com/mcp
    headers:
      Authorization: Bearer xyz
`)
	rt, err := New(ctx, b, stubLLM{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if rt == nil {
		t.Fatal("got nil runtime")
	}
}

func TestModelResolverCalledPerBotInTree(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	childPath := filepath.Join(dir, "child.yml")
	err := os.WriteFile(childPath, []byte("name: child\nsystem: s\n"), 0o600)
	if err != nil {
		t.Fatalf("write child: %v", err)
	}
	parentPath := filepath.Join(dir, "parent.yml")
	parentYAML := "name: parent\nsystem: s\nagents:\n  helper:\n    file: child.yml\n"
	err = os.WriteFile(parentPath, []byte(parentYAML), 0o600)
	if err != nil {
		t.Fatalf("write parent: %v", err)
	}
	b, err := bot.Load(parentPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	seen := map[string]int{}
	resolver := func(bb *bot.Bot) (adkmodel.LLM, error) {
		seen[bb.Name]++
		return stubLLM{}, nil
	}
	_, err = New(ctx, b, stubLLM{}, WithModelResolver(resolver))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if seen["parent"] != 1 || seen["child"] != 1 {
		t.Errorf("resolver call counts = %v, want parent=1 child=1", seen)
	}
}

func TestModelResolverErrorPropagates(t *testing.T) {
	ctx := context.Background()
	b := writeBot(t, `
name: b
system: s
`)
	resolver := func(*bot.Bot) (adkmodel.LLM, error) {
		return nil, errResolver
	}
	_, err := New(ctx, b, stubLLM{}, WithModelResolver(resolver))
	if err == nil {
		t.Fatal("expected resolver error to propagate")
	}
	if !strings.Contains(err.Error(), "resolve model") {
		t.Errorf("error = %v, want to mention 'resolve model'", err)
	}
}

var errResolver = resolverErr("boom")

type resolverErr string

func (e resolverErr) Error() string { return string(e) }
