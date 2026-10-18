package ptysess

import (
	"io"
	"testing"
	"time"
)

// REGRESSION (verified against a real freeze dump, 2026-06-06): a hosted program
// that emits a terminal query — here cursor-position DSR "\033[6n" — made the vt
// emulator write its auto-reply into an undrained io.Pipe and block, INSIDE
// broadcast while holding s.mu. That deadlocked the whole session: vim/claude/htop
// froze, and input died because HandleInput→markDriver also needs s.mu.
// New() must continuously drain the emulator so Write never blocks. This test
// hangs on the buggy code (3s timeout) and passes once the drain is in place.
func TestTerminalQueryDoesNotDeadlock(t *testing.T) {
	s, err := New([]string{"sh", "-c", "printf '\\033[6n'; sleep 5"}, "host", false)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer s.End()
	s.Attach("host", io.Discard, 80, 24, true, true)
	time.Sleep(600 * time.Millisecond) // let readLoop process the query

	// Names() takes s.mu. If broadcast is wedged in vt.Write holding s.mu, it hangs.
	done := make(chan struct{})
	go func() { _ = s.Names(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("DEADLOCK: s.mu held during a terminal-query reply — the session is frozen")
	}
}
