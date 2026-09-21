package ptymux

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Why this file exists.
//
// Claude Code brackets every repaint in ?2026h … ?2026l (synchronized output) and draws inside it
// with PURELY RELATIVE cursor motion. Captured from a real session while typing one line: 44
// frames, zero absolute positioning, zero DECSC. A frame is therefore a transaction whose meaning
// depends on the cursor being exactly where the child left it.
//
// The status bar used to paint whenever it liked. Landing inside a frame composes the bar into the
// child's own atomic update — the tab ribbon appears mid-screen, and every relative move the child
// makes afterwards is offset, which is the overlapping input line.
//
// A verbatim frame from that capture, used below as the fixture:
//
//	ESC[?2026h ESC[?25l ESC[2D ESC[5B \r ESC[2C ESC[5A h \r\n×5 ESC[3C ESC[5A ESC[?25h ESC[?2026l
const realFrame = "\x1b[?2026h\x1b[?25l\x1b[2D\x1b[5B\r\x1b[2C\x1b[5Ah\r\r\n\r\n\r\n\r\n\r\n\x1b[3C\x1b[5A\x1b[?25h\x1b[?2026l"

func TestRealClaudeFrameOpensAndClosesTheWindow(t *testing.T) {
	var m modeState

	// Mid-frame is only true BETWEEN the brackets. Feeding the whole frame at once must leave the
	// child settled — otherwise every paint after a complete write would be needlessly deferred.
	m.observe([]byte(realFrame))
	if m.midFrame() {
		t.Fatal("a complete frame left the child marked mid-frame; the bar would never paint")
	}

	// Split at the point that matters: after the open, before the close.
	cut := strings.Index(realFrame, "\x1b[?25h")
	var m2 modeState
	m2.observe([]byte(realFrame[:cut]))
	if !m2.midFrame() {
		t.Fatal("an open frame was not detected — the bar would paint into the child's update")
	}
	m2.observe([]byte(realFrame[cut:]))
	if m2.midFrame() {
		t.Fatal("the closing bracket did not release the frame")
	}
}

// A lost closing bracket must not wedge the status bar off the screen forever. observe() only sees
// sequences contained in a single write, so a ?2026l split across two reads IS reachable.
// Deferring is an optimisation; a permanently stale bar is a visible bug.
func TestAStuckFrameStopsBlockingAfterTheGrace(t *testing.T) {
	m := modeState{inFrame: true, frameOpen: time.Now()}
	if !m.midFrame() {
		t.Fatal("a fresh frame should block a paint")
	}
	m.frameOpen = time.Now().Add(-frameGrace - time.Millisecond)
	if m.midFrame() {
		t.Fatal("a frame older than the grace window still blocks; the bar would never repaint")
	}
}

// 2026 is frame TIMING, not a mode to re-assert. Replaying "begin synchronized update" on a tab
// switch would open a bracket nobody ever closes — the terminal would stop presenting updates.
func TestFrameModeIsNeverReAsserted(t *testing.T) {
	var m modeState
	m.observe([]byte("\x1b[?2026h\x1b[?25l\x1b[?1000h"))
	got := string(m.restore())
	if strings.Contains(got, "2026") {
		t.Fatalf("restore() replays a synchronized-update bracket: %q", got)
	}
	// The modes that ARE restorable must still survive alongside it.
	if !strings.Contains(got, "1000") {
		t.Fatalf("tracking 2026 broke mouse-mode restore: %q", got)
	}
}

// The end-to-end property: a bar composed while the child is mid-frame reaches the terminal only
// after the frame closes, and lands intact.
func TestBarIsHeldUntilTheChildsFrameCloses(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	mx := &Mux{}
	g := &gate{out: &mx.outMu, mx: mx, active: true}

	// The child opens a frame; the bar is composed during it.
	cut := strings.Index(realFrame, "\x1b[?25h")
	g.Write([]byte(realFrame[:cut]))

	mx.outMu.Lock()
	mx.barBytes = []byte("<<BAR>>")
	mx.barPending = g.modes.midFrame()
	mx.outMu.Unlock()
	if !mx.barPending {
		t.Fatal("the bar was not deferred during an open frame")
	}

	// The child closes it — that write is what releases the bar.
	g.Write([]byte(realFrame[cut:]))

	w.Close()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	if !strings.Contains(out, "<<BAR>>") {
		t.Fatal("the deferred bar never reached the terminal")
	}
	// The decisive assertion: the bar sits AFTER the frame's closing bracket, never inside it.
	if strings.Index(out, "<<BAR>>") < strings.Index(out, "\x1b[?2026l") {
		t.Fatal("the bar was written inside the child's synchronized-update frame")
	}
	if mx.barPending {
		t.Fatal("barPending was not cleared after the flush")
	}
}
