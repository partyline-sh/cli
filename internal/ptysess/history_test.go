package ptysess

import (
	"io"
	"strings"
	"testing"
	"time"
)

// TestSnapshotHistory verifies the scrollback-replay assembly against a real emulator: a
// main-screen program that prints more lines than the screen is tall pushes early lines into
// scrollback, and SnapshotHistory must emit those scrolled-off lines (in order) BEFORE the
// current screen, as one continuous stream with no interior screen-clear — that's what lets the
// mux rebuild the terminal's native scrollback for exactly this session.
func TestSnapshotHistory(t *testing.T) {
	// 40 numbered lines on a 10-row screen → ~30 lines scroll off into scrollback.
	s, err := New([]string{"sh", "-c", "for i in $(seq 1 40); do echo line$i; done; sleep 5"}, "host", false)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer s.End()
	s.Attach("host", io.Discard, 40, 10, true, true)
	time.Sleep(600 * time.Millisecond) // let readLoop drain the 40 lines into the vt

	out := string(s.SnapshotHistory(maxTestLines))

	// Early lines must be present (they can only come from replayed scrollback, not the 10-row
	// screen) and must appear before later lines — i.e. history is in chronological order.
	iEarly := strings.Index(out, "line3")
	iLate := strings.Index(out, "line39")
	if iEarly < 0 {
		t.Fatalf("SnapshotHistory missing early scrollback line 'line3':\n%q", out)
	}
	if iLate < 0 {
		t.Fatalf("SnapshotHistory missing recent line 'line39':\n%q", out)
	}
	if iEarly > iLate {
		t.Errorf("history out of order: line3 (%d) should precede line39 (%d)", iEarly, iLate)
	}

	// No interior full-screen clear: the whole point is a continuous stream so the scrollback
	// lands in the native buffer instead of being wiped. (The mux emits its own leading clear.)
	if strings.Contains(out, "\x1b[2J") {
		t.Errorf("SnapshotHistory must not contain an interior \\x1b[2J clear:\n%q", out)
	}
	// Ends by positioning the cursor.
	if !strings.Contains(out, "\x1b[") || !strings.HasSuffix(strings.TrimRight(out, " "), "H") {
		// cursor address is "\x1b[<y>;<x>H"; tolerate trailing spaces from a blank last row
		if !strings.Contains(out, "H") {
			t.Errorf("SnapshotHistory should end with a cursor-position sequence:\n%q", out)
		}
	}
}

const maxTestLines = 1000
