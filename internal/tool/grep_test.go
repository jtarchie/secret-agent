package tool

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGrepFileSingleMatch(t *testing.T) {
	p := writeTempFile(t, "a.go", "package x\nfunc Foo() {}\nfunc Bar() {}\n")
	out, err := grepWalk(regexp.MustCompile(`Foo`), p, 30)
	if err != nil {
		t.Fatalf("grepWalk: %v", err)
	}
	if !strings.Contains(out, ":2: func Foo() {}") {
		t.Errorf("expected line-2 hit, got %q", out)
	}
	if strings.Contains(out, "Bar") {
		t.Errorf("unexpected match for Bar: %q", out)
	}
}

func TestGrepDirRecurses(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("Foo\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	err = os.Mkdir(sub, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(sub, "b.go"), []byte("nope\nFoo\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	out, err := grepWalk(regexp.MustCompile(`Foo`), dir, 30)
	if err != nil {
		t.Fatalf("grepWalk: %v", err)
	}
	if !strings.Contains(out, "a.go:1") {
		t.Errorf("missing a.go hit: %q", out)
	}
	if !strings.Contains(out, "b.go:2") {
		t.Errorf("missing b.go hit: %q", out)
	}
}

func TestGrepSkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	hidden := filepath.Join(dir, ".git")
	err := os.Mkdir(hidden, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(hidden, "config"), []byte("secret\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("secret\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	out, err := grepWalk(regexp.MustCompile(`secret`), dir, 30)
	if err != nil {
		t.Fatalf("grepWalk: %v", err)
	}
	if strings.Contains(out, ".git") {
		t.Errorf("should skip .git, got %q", out)
	}
	if !strings.Contains(out, "ok.txt") {
		t.Errorf("expected hit on ok.txt, got %q", out)
	}
}

func TestGrepRespectsCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("hit\n")
	}
	p := writeTempFile(t, "many.txt", b.String())
	out, err := grepWalk(regexp.MustCompile(`hit`), p, 5)
	if err != nil {
		t.Fatalf("grepWalk: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// 5 hits + 1 "[truncated at 5 matches]" trailer = 6
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[5], "[truncated at 5") {
		t.Errorf("expected truncation trailer, got %q", lines[5])
	}
}

func TestGrepCapHonorsHardCap(t *testing.T) {
	// max_results > grepHardCap is silently lowered. Inspect by passing
	// many matches and a high requested max.
	var b strings.Builder
	for i := 0; i < grepHardCap+10; i++ {
		b.WriteString("x\n")
	}
	p := writeTempFile(t, "lots.txt", b.String())
	out, err := grepWalk(regexp.MustCompile(`x`), p, grepHardCap)
	if err != nil {
		t.Fatalf("grepWalk: %v", err)
	}
	if !strings.Contains(out, "[truncated at "+itoa(grepHardCap)) {
		t.Errorf("expected truncation at hard cap, got %q", out)
	}
}

func TestGrepLineTruncated(t *testing.T) {
	long := strings.Repeat("a", grepLineMaxRunes+50)
	p := writeTempFile(t, "long.txt", "needle "+long+"\n")
	out, err := grepWalk(regexp.MustCompile(`needle`), p, 30)
	if err != nil {
		t.Fatalf("grepWalk: %v", err)
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), grepLineTruncSfx) {
		t.Errorf("expected line truncation suffix, got tail %q", tail(out, 30))
	}
}

func TestGrepMissingPath(t *testing.T) {
	_, err := grepWalk(regexp.MustCompile("x"), filepath.Join(t.TempDir(), "nope"), 30)
	if err == nil {
		t.Fatal("expected error on missing path")
	}
}

func TestNewGrepBuilds(t *testing.T) {
	tool, err := NewGrep("grep", "")
	if err != nil {
		t.Fatalf("NewGrep: %v", err)
	}
	if tool == nil {
		t.Fatal("expected tool")
	}
}

// itoa is local to keep the test file dependency-free.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
