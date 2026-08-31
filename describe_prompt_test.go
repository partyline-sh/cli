package main

import (
	"bufio"
	"strings"
	"testing"
)

// /quit used to work at only ONE of the three prompts in the describe loop — at the other two it
// was forwarded to the model as if the user had typed a feature request. All three now read through
// describeLine, so this covers every prompt in the loop.
func TestDescribeLineHonoursQuitAtEveryPrompt(t *testing.T) {
	for _, in := range []string{"/quit", "/exit", "/QUIT", "  /quit  "} {
		sc := bufio.NewScanner(strings.NewReader(in + "\n"))
		if line, ok := describeLine(sc); ok {
			t.Errorf("describeLine(%q) = (%q, true), want quit", in, line)
		}
	}
}

func TestDescribeLinePassesNormalInputThrough(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("  it should also handle CSV  \n/done\n"))
	line, ok := describeLine(sc)
	if !ok || line != "it should also handle CSV" {
		t.Fatalf("describeLine = (%q, %v), want the trimmed answer", line, ok)
	}
	// The mode/emit commands are NOT swallowed here — the caller's switch owns those.
	line, ok = describeLine(sc)
	if !ok || line != "/done" {
		t.Fatalf("describeLine = (%q, %v), want /done passed through", line, ok)
	}
}

// A closed stdin is a quit, not a fatal.
func TestDescribeLineEOFQuits(t *testing.T) {
	if _, ok := describeLine(bufio.NewScanner(strings.NewReader(""))); ok {
		t.Fatal("describeLine on EOF must report quit")
	}
}

// The progress line must never go into a pipe or a log — under `go test` stdout isn't a tty, so
// the stopper is a no-op and nothing is ever written.
func TestDescribeWaitingIsSilentOffTerminal(t *testing.T) {
	stop := describeWaiting("the engine")
	stop() // must not block, must not panic, must not print
}
