package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/brand"
)

// The defect this whole file exists for: the old ask_peer wait never read stdin, so no key could
// end it. These tests drive a waitJob with a fake key source and a fake clock — if the wait ever
// stops consulting its input, they hang or return the wrong outcome.

func TestWaitCancelsOnEscWithoutBlocking(t *testing.T) {
	keys := make(chan rune, 1)
	keys <- 0 // 0 is what decodeKey gives a lone esc / ctrl-c / ctrl-\
	polls := 0
	done := make(chan waitOutcome, 1)
	go func() {
		out, _ := waitJob{
			What: "asking mac-studio about acr-cloud", Ceiling: time.Hour,
			Poll: time.Hour, Tick: time.Hour, // neither the poll nor the tick can end this — only the key
			Check: func() (bool, error) { polls++; return false, nil },
			keys:  keys, line: func(string) {},
		}.Run()
		done <- out
	}()
	select {
	case out := <-done:
		if out != waitCancelled {
			t.Fatalf("outcome = %v, want waitCancelled", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wait never returned — it is not reading its input")
	}
	if polls != 1 {
		t.Fatalf("polls = %d, want 1 (one up-front poll, then the key)", polls)
	}
}

// 'q' cancels too — the same two keys every other surface honours.
func TestWaitCancelsOnQ(t *testing.T) {
	keys := make(chan rune, 1)
	keys <- 'q'
	out, err := waitJob{Ceiling: time.Hour, Poll: time.Hour, Tick: time.Hour,
		Check: func() (bool, error) { return false, nil },
		keys:  keys, line: func(string) {}}.Run()
	if out != waitCancelled || err != nil {
		t.Fatalf("= (%v, %v), want (waitCancelled, nil)", out, err)
	}
}

// An unrelated keystroke must NOT end the wait — it keeps going and the ceiling still applies.
func TestWaitIgnoresOtherKeysAndHonoursTheCeiling(t *testing.T) {
	keys := make(chan rune, 4)
	keys <- 'x'
	keys <- '\n'
	clock := time.Now()
	out, _ := waitJob{Ceiling: 10 * time.Second, Poll: time.Hour, Tick: time.Millisecond,
		Check: func() (bool, error) { return false, nil },
		keys:  keys, line: func(string) {},
		now: func() time.Time { clock = clock.Add(4 * time.Second); return clock }}.Run()
	if out != waitExpired {
		t.Fatalf("outcome = %v, want waitExpired", out)
	}
}

func TestWaitDoneAndFailed(t *testing.T) {
	out, err := waitJob{Ceiling: time.Hour, Tick: time.Millisecond,
		Check: func() (bool, error) { return true, nil }, keys: make(chan rune), line: func(string) {}}.Run()
	if out != waitDone || err != nil {
		t.Fatalf("= (%v, %v), want (waitDone, nil)", out, err)
	}
	boom := errors.New("no route to host")
	out, err = waitJob{Ceiling: time.Hour, Tick: time.Millisecond,
		Check: func() (bool, error) { return false, boom }, keys: make(chan rune), line: func(string) {}}.Run()
	if out != waitFailed || !errors.Is(err, boom) {
		t.Fatalf("= (%v, %v), want (waitFailed, the poll error)", out, err)
	}
}

// A closed key source (EOF, no tty) must not spin or wedge: the wait falls back to the clock.
func TestWaitSurvivesAClosedKeySource(t *testing.T) {
	keys := make(chan rune)
	close(keys)
	clock := time.Now()
	out, _ := waitJob{Ceiling: 5 * time.Second, Poll: time.Hour, Tick: time.Millisecond,
		Check: func() (bool, error) { return false, nil }, keys: keys, line: func(string) {},
		now: func() time.Time { clock = clock.Add(2 * time.Second); return clock }}.Run()
	if out != waitExpired {
		t.Fatalf("outcome = %v, want waitExpired", out)
	}
}

func TestWaitLineFormat(t *testing.T) {
	got := waitLine("asking mac-studio about acr-cloud", 42*time.Second, 108*time.Second)
	for _, want := range []string{"asking mac-studio about acr-cloud", "0:42 elapsed", "1:48 left", "esc cancel"} {
		if !strings.Contains(got, want) {
			t.Errorf("waitLine missing %q: %q", want, got)
		}
	}
	// Escapes must not count toward the width, or a wide terminal's line wraps on colour alone.
	if w := brand.VisWidth(got); w > 90 {
		t.Errorf("waitLine visible width = %d, too wide: %q", w, got)
	}
	// No ceiling ⇒ no "left" claim.
	if s := waitLine("x", time.Second, -1); strings.Contains(s, "left") {
		t.Errorf("no-ceiling line must not claim a remaining time: %q", s)
	}
}

func TestMmssFloorsNegatives(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{0, "0:00"}, {59 * time.Second, "0:59"}, {60 * time.Second, "1:00"},
		{-3 * time.Second, "0:00"}, {605 * time.Second, "10:05"}} {
		if got := mmss(c.d); got != c.want {
			t.Errorf("mmss(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// Under `go test` stdout is not a terminal, which is exactly when a redraw-in-place line is noise.
// liveLine must write NOTHING there — and it must not panic on the erase call either.
func TestLiveLineIsSilentOffTTY(t *testing.T) {
	if stdoutIsTTY() {
		t.Skip("stdout is a terminal here")
	}
	liveLine("should not appear")
	liveLine("")
}

// waitKeys with no tty (go test) hands back a dead channel and a no-op stop, so nothing hangs.
func TestWaitKeysWithoutATTY(t *testing.T) {
	if stdinIsTTY() {
		t.Skip("stdin is a terminal here")
	}
	ch, stop := waitKeys()
	defer stop()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("waitKeys produced a key with no tty")
		}
	case <-time.After(time.Second):
		t.Fatal("waitKeys channel must be closed when there is no tty, not left open")
	}
}

func TestDecodeKeyMatchesMenuKeyRules(t *testing.T) {
	cases := []struct {
		in   []byte
		want rune
		ok   bool
	}{
		{[]byte{0x1b}, 0, true},            // lone esc → cancel
		{[]byte{0x1b, '[', 'A'}, 0, false}, // up arrow → ignored, never a cancel
		{[]byte{0x03}, 0, true},            // ctrl-c
		{[]byte{0x1c}, 0, true},            // ctrl-\
		{[]byte{'\r'}, '\n', true},         // enter folds to \n
		{[]byte{'Q'}, 'q', true},           // case folded
		{[]byte{'n'}, 'n', true},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := decodeKey(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("decodeKey(%v) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
