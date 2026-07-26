package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// These drive the real readers over a real pty, because the things worth pinning are terminal
// behaviours: does ONE keystroke select, does esc always get you out, and is the tty handed back
// exactly as it was found. A fake reader would prove none of that.
//
// stdout goes to a file, not the pty — the paint is tens of kilobytes per redraw and would fill the
// pty buffer. A file also makes term.GetSize fail, so the modals lay out at the 80×24 fallback.

// cgDrive feeds keys to a modal reader and returns what it painted. It fails the test if the reader
// hasn't returned in a few seconds: a modal you can't get out of is the bug this file guards.
func cgDrive(t *testing.T, keys string, fn func()) string {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	out, err := os.CreateTemp(t.TempDir(), "paint")
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = tty, out
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	before, err := term.GetState(int(tty.Fd()))
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	// The keys go in AFTER the reader has taken the tty raw. Written while the line discipline is
	// still cooked, ctrl-c and ctrl-\ would be eaten as signals instead of arriving as the bytes the
	// escape contract is built on.
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	time.Sleep(100 * time.Millisecond)
	if _, err := ptmx.WriteString(keys); err != nil {
		t.Fatalf("feeding keys: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("the modal never returned for keys %q — it can be got stuck in", keys)
	}

	// EVERY exit path restores the terminal. Raw mode leaking out of a modal is how the mux's
	// repainted session ends up with no echo.
	after, err := term.GetState(int(tty.Fd()))
	if err != nil {
		t.Fatalf("GetState after: %v", err)
	}
	if *before != *after {
		t.Errorf("terminal state was not restored after keys %q", keys)
	}
	b, _ := os.ReadFile(out.Name())
	return string(b)
}

func cgTestPicker() cgPicker {
	return cgPicker{Title: "Ask a peer", Items: []string{"one", "two", "three"}, Verb: "pick a peer",
		Extras: []cgChoice{{Key: 'n', Label: "ask someone new"}}}
}

// A bare digit selects. No enter, no typed number — the whole point of reading raw.
func TestPickerSelectsOnASingleDigit(t *testing.T) {
	var idx int
	var key rune
	var ok bool
	paint := cgDrive(t, "2", func() { idx, key, ok = cgTestPicker().run() })
	if !ok || idx != 1 || key != 0 {
		t.Fatalf("run() = (%d, %q, %v), want (1, 0, true)", idx, key, ok)
	}
	if !strings.Contains(cgPlain(paint), "two") {
		t.Error("the list must be part of the painted frame")
	}
}

// esc and q both leave, on every screen. Enter closes too — the contract cgExitHints documents.
func TestPickerAlwaysHasAWayOut(t *testing.T) {
	for _, keys := range []string{"\x1b", "q", "Q", "\r", "\x03", "\x1c"} {
		var idx int
		var ok bool
		cgDrive(t, keys, func() { idx, _, ok = cgTestPicker().run() })
		if ok || idx != -1 {
			t.Errorf("keys %q selected (%d, %v) instead of getting out", keys, idx, ok)
		}
	}
}

// An out-of-range digit selects nothing, crashes nothing, and does NOT silently cancel the way the
// old Pick did — you stay on the screen and can try again.
func TestPickerIgnoresAnOutOfRangeDigit(t *testing.T) {
	var idx int
	var ok bool
	cgDrive(t, "9\x1b", func() { idx, _, ok = cgTestPicker().run() })
	if ok || idx != -1 {
		t.Errorf("'9' on a 3-item list gave (%d, %v)", idx, ok)
	}
	// …and the screen was still live: the following '2' selects.
	cgDrive(t, "92", func() { idx, _, ok = cgTestPicker().run() })
	if !ok || idx != 1 {
		t.Errorf("after an out-of-range digit the picker should still select: (%d, %v)", idx, ok)
	}
	// '0' is never an item either.
	cgDrive(t, "0q", func() { idx, _, ok = cgTestPicker().run() })
	if ok {
		t.Errorf("'0' selected %d", idx)
	}
}

// An extra letter key is its own outcome, not an index.
func TestPickerReturnsExtraKeys(t *testing.T) {
	var idx int
	var key rune
	var ok bool
	cgDrive(t, "n", func() { idx, key, ok = cgTestPicker().run() })
	if !ok || key != 'n' || idx != -1 {
		t.Fatalf("run() = (%d, %q, %v), want (-1, 'n', true)", idx, key, ok)
	}
}

// Past 9 items a bare digit can't select — it might be the start of "12" — so the number is typed
// and confirmed. Both digits must survive even when they arrive in ONE read.
func TestPickerTakesTypedNumbersPastNine(t *testing.T) {
	p := cgPicker{Title: "Switch thread", Verb: "attach"}
	for i := 0; i < 12; i++ {
		p.Items = append(p.Items, "thread")
	}
	var idx int
	var ok bool
	cgDrive(t, "1\x1b", func() { idx, _, ok = p.run() })
	if ok {
		t.Errorf("a bare '1' must not select on a 12-item list: (%d, %v)", idx, ok)
	}
	cgDrive(t, "12\r", func() { idx, _, ok = p.run() })
	if !ok || idx != 11 {
		t.Fatalf("typed 12 gave (%d, %v), want (11, true)", idx, ok)
	}
	// Out of range clears the field instead of cancelling; the next attempt still works.
	cgDrive(t, "99\r7\r", func() { idx, _, ok = p.run() })
	if !ok || idx != 6 {
		t.Fatalf("after an out-of-range 99 the picker gave (%d, %v), want (6, true)", idx, ok)
	}
}

// The text field echoes INSIDE the frame and hands back the line. esc cancels it; so does a bare q,
// which is the only reading of q that can't collide with typing one.
func TestAskEchoesInTheFrameAndCancels(t *testing.T) {
	var got string
	var ok bool
	paint := cgDrive(t, "hi there\r", func() { got, ok = cgAsk("Ask a peer", nil, "your question", "") })
	if !ok || got != "hi there" {
		t.Fatalf("cgAsk = (%q, %v)", got, ok)
	}
	if !strings.Contains(cgPlain(paint), "hi there") {
		t.Error("typed text must be echoed into the frame, not printed under it")
	}
	for _, keys := range []string{"\x1b", "q\r", "\r", "\x03", "abc\x7f\x7f\x7f\r", "abc\x15\r"} {
		cgDrive(t, keys, func() { got, ok = cgAsk("Ask a peer", nil, "your question", "") })
		if ok {
			t.Errorf("keys %q returned %q instead of cancelling", keys, got)
		}
	}
	// A default is what enter takes.
	cgDrive(t, "\r", func() { got, ok = cgAsk("New thread", nil, "title", "note") })
	if !ok || got != "note" {
		t.Errorf("enter on a defaulted field = (%q, %v), want (\"note\", true)", got, ok)
	}
}

// Confirm: y, n, enter-for-the-default, and three ways to cancel — all one keystroke.
func TestConfirmInFrame(t *testing.T) {
	cases := []struct {
		keys     string
		wantVal  bool
		wantOkay bool
	}{
		{keys: "y", wantVal: true, wantOkay: true},
		{keys: "n", wantVal: false, wantOkay: true},
		{keys: "\r", wantVal: false, wantOkay: true}, // default is no
		{keys: "\x1b", wantOkay: false},
		{keys: "q", wantOkay: false},
		{keys: "\x03", wantOkay: false},
	}
	for _, c := range cases {
		var val, ok bool
		cgDrive(t, c.keys, func() { val, ok = cgConfirm("Fork into worktree", nil, "carry your work?", false) })
		if val != c.wantVal || ok != c.wantOkay {
			t.Errorf("keys %q = (%v, %v), want (%v, %v)", c.keys, val, ok, c.wantVal, c.wantOkay)
		}
	}
}

// With no tty there is nothing to read, so a modal returns at once rather than blocking a pipe or
// the daemon — the same rule menuKey has always followed.
func TestModalReadersDoNotBlockWithoutATTY(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin = r
	if out, e := os.CreateTemp(t.TempDir(), "paint"); e == nil {
		os.Stdout = out
	}
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, ok := cgTestPicker().run(); ok {
			t.Error("a picker with no tty must not select")
		}
		if _, ok := cgAsk("t", nil, "l", ""); ok {
			t.Error("cgAsk with no tty must not return an answer")
		}
		if _, ok := cgConfirm("t", nil, "l", true); ok {
			t.Error("cgConfirm with no tty must not confirm")
		}
		cgNote("t", []string{"body"})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a modal blocked with no tty on the other end")
	}
}
