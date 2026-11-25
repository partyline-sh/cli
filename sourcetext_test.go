package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Source files must be TEXT, because a file containing a raw control byte is BINARY to grep — and
// grep silently SKIPS binary files rather than erroring. The file compiles, its tests pass, and
// every codebase search reports its symbols as nonexistent.
//
// Not hypothetical. web/src/lib/api/work-view.ts held three raw NUL bytes as a "missing value" sort
// sentinel, written as the byte instead of the escape. That file defines PlanningCard, WorkCard and
// chainColor — and grepping the repo for any of them returned NOTHING. That is exactly the kind of
// false answer that sends an agent off rebuilding something that already exists, or reporting a
// feature as missing when it is right there.
//
// The characters are identical either way; only the ENCODING in the source differs. Write the
// escape, never the byte.
func TestSourceFilesAreGreppable(t *testing.T) {
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	binaryExt := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true, ".icns": true,
		".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".pdf": true, ".zip": true,
		".gz": true, ".webp": true, ".webm": true, ".mp4": true, ".wasm": true,
	}
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f == "" || binaryExt[strings.ToLower(filepath.Ext(f))] {
			continue
		}
		info, err := os.Stat(f)
		if err != nil || info.IsDir() {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		// A compiled binary has no extension to filter on. Skip anything mostly non-text rather
		// than reporting it as a source defect.
		if looksBinary(b) {
			continue
		}
		if i := indexControl(b); i >= 0 {
			t.Errorf("%s: raw control byte 0x%02x at line %d — this file is BINARY to grep, so every "+
				"code search silently skips it and its symbols look nonexistent. Write it as an escape "+
				"instead; the character is the same.", f, b[i], 1+strings.Count(string(b[:i]), "\n"))
		}
	}
}

// indexControl returns the first position holding a control character other than tab, newline or
// carriage return, or -1.
func indexControl(b []byte) int {
	for i, c := range b {
		if c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c < 32 || c == 127 {
			return i
		}
	}
	return -1
}

// looksBinary reports whether a blob is mostly non-text, so a checked-in executable is not reported
// as a source file with a stray byte. Deliberately crude: the goal is to exempt obvious binaries.
func looksBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	ctrl := 0
	for _, c := range b[:n] {
		if c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c < 32 || c == 127 {
			ctrl++
		}
	}
	return n > 0 && ctrl*100/n > 5
}
