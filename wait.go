package main

import (
	"fmt"
	"time"
)

// A bounded, CANCELLABLE wait with a live progress line — the one thing every "we're blocking on
// something remote" surface in this codebase needs and none of them had. The bug it exists to kill:
// the old ask_peer wait was `for { poll; sleep(2s) }` with stdin NEVER READ, so no keystroke could
// end it and the only way out was killing the whole mux. Reading stdin concurrently is the fix;
// everything else here (the elapsed line, the ceiling) is what tells "working" from "hung".
//
// There are ~12 other blocking waits that want this. Deliberately not retrofitted here.

type waitOutcome int

const (
	waitDone      waitOutcome = iota // Check reported finished
	waitCancelled                    // esc / q / ctrl-c / ctrl-\
	waitExpired                      // hit Ceiling
	waitFailed                       // Check returned an error
)

// waitJob is one wait. Check is polled every Poll until it says done; the live line is redrawn
// every Tick; a key ends it at ANY point. The unexported fields are test seams — a fake key
// source and a fake clock are how we prove stdin is actually read without a terminal.
type waitJob struct {
	What    string               // "asking mac-studio about acr-cloud"
	Ceiling time.Duration        // hard stop; 0 means no ceiling
	Poll    time.Duration        // Check cadence (default 2s)
	Tick    time.Duration        // live-line cadence (default 500ms)
	Check   func() (bool, error) // the remote poll

	keys <-chan rune      // nil ⇒ raw stdin
	now  func() time.Time // nil ⇒ time.Now
	line func(string)     // nil ⇒ liveLine (silent off-tty)
}

// Run blocks until Check finishes, a key cancels, or the ceiling passes. It never blocks longer
// than Tick without checking for a keystroke, which is the whole point.
func (j waitJob) Run() (waitOutcome, error) {
	if j.Poll <= 0 {
		j.Poll = 2 * time.Second
	}
	if j.Tick <= 0 {
		j.Tick = 500 * time.Millisecond
	}
	now, draw := j.now, j.line
	if now == nil {
		now = time.Now
	}
	if draw == nil {
		draw = liveLine
	}
	keys := j.keys
	if keys == nil {
		k, stop := waitKeys()
		defer stop()
		keys = k
	}

	start := now()
	tick := time.NewTicker(j.Tick)
	defer tick.Stop()
	nextPoll := start // poll once immediately: the answer may already be there
	for {
		t := now()
		if j.Ceiling > 0 && !t.Before(start.Add(j.Ceiling)) {
			draw("")
			return waitExpired, nil
		}
		if !t.Before(nextPoll) {
			done, err := j.Check()
			if err != nil {
				draw("")
				return waitFailed, err
			}
			if done {
				draw("")
				return waitDone, nil
			}
			nextPoll = t.Add(j.Poll)
		}
		left := time.Duration(-1) // no ceiling ⇒ the line must not claim a remaining time
		if j.Ceiling > 0 {
			left = start.Add(j.Ceiling).Sub(t)
		}
		draw(waitLine(j.What, t.Sub(start), left))
		select {
		case k, ok := <-keys:
			if !ok { // the key source died (EOF / no tty) — keep waiting on the clock alone
				keys = nil
				continue
			}
			if k == 0 || k == 'q' { // 0 = esc / ctrl-c / ctrl-\ (menuKey's rules)
				draw("")
				return waitCancelled, nil
			}
		case <-tick.C:
		}
	}
}

// waitLine is the progress row: what we're waiting on, how long it's been, how long is left, and
// the key that ends it. "0:42 elapsed · 1:48 left" is the difference between a user waiting and a
// user reaching for ctrl-c. A negative `left` means there's no ceiling, so no remaining is claimed.
func waitLine(what string, elapsed, left time.Duration) string {
	tail := mmss(elapsed) + " elapsed · "
	if left >= 0 {
		tail += mmss(left) + " left · "
	}
	return "  " + sgr(cgWire, "☎") + " " + what + " …  " + dim(tail+"esc cancel")
}

// mmss formats a duration as m:ss, flooring negatives to 0:00 (a passed deadline isn't "-0:01").
func mmss(d time.Duration) string {
	s := int(d / time.Second)
	if s < 0 {
		s = 0
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// liveLine redraws ONE line in place. Silent when stdout isn't a terminal: a carriage-returned
// progress line in a log or a pipe is unreadable noise, and \r\x1b[K in a captured transcript is
// worse than nothing. Empty s erases the line.
func liveLine(s string) {
	if !stdoutIsTTY() {
		return
	}
	if s == "" {
		fmt.Print("\r\x1b[K")
		return
	}
	fmt.Print("\r\x1b[K" + s)
}
