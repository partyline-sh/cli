package main

import (
	"os"
	"testing"
)

// Under `go test` stdin is never a tty, which is exactly the case that used to hang or fatal:
// every helper must CANCEL there, promptly and without touching the process.

func TestConfirmCancelsWithoutATTY(t *testing.T) {
	val, ok := Confirm("delete everything", true)
	if ok {
		t.Fatal("Confirm reported ok=true with no tty — a non-interactive answer must never count")
	}
	if val {
		t.Fatal("Confirm returned true on cancel — cancel must never read as yes, even with def=true")
	}
}

// A cancelled Confirm is distinguishable from a deliberate no: both give val=false, but only the
// deliberate one gives ok=true. Callers rely on that to tell "unwind" from "proceed differently".
func TestConfirmCancelIsNotADeliberateNo(t *testing.T) {
	if _, ok := Confirm("q", false); ok {
		t.Fatal("cancel must yield ok=false")
	}
}

func TestInputCancelsWithoutATTY(t *testing.T) {
	if got, ok := Input("name", ""); ok || got != "" {
		t.Fatalf("Input with no tty = (%q, %v), want (\"\", false)", got, ok)
	}
	// Even WITH a default: there's nobody to accept it, so the caller must not silently proceed.
	if got, ok := Input("name", "fallback"); ok || got != "" {
		t.Fatalf("Input with a default and no tty = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestPickRejectsOutOfRangeAndEmpty(t *testing.T) {
	items := []string{"one", "two", "three"}
	// No tty → Input cancels → Pick cancels. The key property is that it RETURNS: the old pickers
	// called fatal() here, killing the process over a typo.
	idx, ok := Pick("number", items, func(s string) string { return s })
	if ok || idx != -1 {
		t.Fatalf("Pick = (%d, %v), want (-1, false)", idx, ok)
	}
	// An empty list is a cancel, not a panic.
	if idx, ok := Pick("number", []string{}, func(s string) string { return s }); ok || idx != -1 {
		t.Fatalf("Pick on empty list = (%d, %v), want (-1, false)", idx, ok)
	}
}

func TestConfirmDestructiveRefusesWhenItCannotAsk(t *testing.T) {
	if confirmDestructive("delete the repo", false) {
		t.Fatal("confirmDestructive proceeded with no terminal to confirm on")
	}
	// --yes is the documented way through, and it must not depend on a tty.
	if !confirmDestructive("delete the repo", true) {
		t.Fatal("confirmDestructive ignored --yes")
	}
}

func TestTakeYesFlag(t *testing.T) {
	for _, c := range []struct {
		in       []string
		wantArgs int
		wantYes  bool
	}{
		{[]string{"branch"}, 1, false},
		{[]string{"branch", "--yes"}, 1, true},
		{[]string{"-y", "branch"}, 1, true},
		{[]string{"--clear"}, 1, false},
	} {
		args, yes := takeYesFlag(c.in)
		if len(args) != c.wantArgs || yes != c.wantYes {
			t.Errorf("takeYesFlag(%v) = (%v, %v), want (%d args, %v)", c.in, args, yes, c.wantArgs, c.wantYes)
		}
	}
}

func TestColorGate(t *testing.T) {
	// stdout under `go test` is not a tty, so colour is off and sgr is the identity function —
	// this is the log-corruption case (escapes leaking into a pipe or a file).
	if colorOK() {
		t.Fatal("colorOK() true with a non-tty stdout — escapes would leak into pipes and logs")
	}
	if got := sgr(cgOK, "ok"); got != "ok" {
		t.Fatalf("sgr with colour off = %q, want the string untouched", got)
	}
	if got := dim("hint"); got != "hint" {
		t.Fatalf("dim with colour off = %q, want the string untouched", got)
	}

	// NO_COLOR is honoured independently of the tty check (no-color.org).
	t.Setenv("NO_COLOR", "1")
	if colorOK() {
		t.Fatal("colorOK() true with NO_COLOR set")
	}
	// Unset again and it's still false here — because stdout is still not a tty.
	t.Setenv("NO_COLOR", "")
	if colorOK() != stdoutIsTTY() {
		t.Fatal("with NO_COLOR empty, colorOK must reduce to the tty check")
	}
	if os.Getenv("NO_COLOR") != "" {
		t.Fatal("t.Setenv did not clear NO_COLOR")
	}
}
