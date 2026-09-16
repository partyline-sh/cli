package ptysess

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

// syncBuf is a concurrency-safe io.Writer (the per-participant writer goroutine
// writes while the test reads).
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuf) Write(p []byte) (int, error) { w.mu.Lock(); defer w.mu.Unlock(); return w.b.Write(p) }
func (w *syncBuf) String() string              { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }

// Everyone on the line sees who's typing via their terminal title (OSC), and the
// typer sees "you're typing". This is the ambient who's-driving indicator.
func TestTypingIndicatorTitles(t *testing.T) {
	s := newTestSession(false)
	var hostBuf, viewerBuf syncBuf
	host := s.Attach("darcy", &hostBuf, 0, 0, true, true)
	s.Attach("joe", &viewerBuf, 0, 0, false, true)
	s.markDriver(host) // darcy types

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(viewerBuf.String(), "darcy typing") && strings.Contains(hostBuf.String(), "you're typing") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(viewerBuf.String(), "\x1b]0;") || !strings.Contains(viewerBuf.String(), "✎ darcy typing") {
		t.Errorf("viewer title should show 'darcy typing'; got %q", viewerBuf.String())
	}
	if !strings.Contains(hostBuf.String(), "you're typing") {
		t.Errorf("typer's title should show 'you're typing'; got %q", hostBuf.String())
	}
}

// newTestSession builds a Session WITHOUT a real pty/program (New() would spawn a
// shell). That's enough to exercise the access-control surface — Attach only
// touches the pty via recalcSize, which we avoid by attaching with cols/rows = 0.
func newTestSession(open bool) *Session {
	return &Session{
		parts:       make(map[int64]*Participant),
		vt:          vt.NewSafeEmulator(80, 24),
		open:        open,
		seenDrivers: make(map[string]struct{}),
		Done:        make(chan struct{}),
	}
}

func attach(s *Session, name string, isHost, fullAccess bool) *Participant {
	return s.Attach(name, io.Discard, 0, 0, isHost, fullAccess)
}

// THE core invariant: a viewer (not full-access) is NEVER typeable — not on a
// closed line, not on an open one. Host and full-access behave as specified.
func TestAttach_CanTypeMatrix(t *testing.T) {
	cases := []struct {
		name       string
		open       bool
		isHost     bool
		fullAccess bool
		wantType   bool
		wantFull   bool
	}{
		{"viewer, closed line", false, false, false, false, false},
		{"viewer, OPEN line — still watch-only", true, false, false, false, false},
		{"full-access, closed line — needs grant", false, false, true, false, true},
		{"full-access, open line — types freely", true, false, true, true, true},
		{"host always types", false, true, false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestSession(c.open)
			p := attach(s, "p", c.isHost, c.fullAccess)
			if p.CanType != c.wantType {
				t.Errorf("CanType = %v, want %v", p.CanType, c.wantType)
			}
			if p.FullAccess != c.wantFull {
				t.Errorf("FullAccess = %v, want %v", p.FullAccess, c.wantFull)
			}
		})
	}
}

// A viewer can never be granted typing — not even by the host.
func TestGrantToggle_ViewerRefused(t *testing.T) {
	s := newTestSession(false)
	attach(s, "vic", false, false) // viewer
	name, now, res := s.GrantToggle("vic")
	if res != "viewer" {
		t.Fatalf("result = %q, want viewer", res)
	}
	if now {
		t.Error("viewer became typeable after grant — paid/security wall broken")
	}
	if name != "vic" {
		t.Errorf("name = %q, want vic", name)
	}
}

func TestGrantToggle_FullAccessTogglesAndIsCaseInsensitive(t *testing.T) {
	s := newTestSession(false)
	p := attach(s, "Dev", false, true) // full-access, closed line → CanType false
	if p.CanType {
		t.Fatal("full-access on a closed line should start non-typing")
	}
	if _, now, res := s.GrantToggle("dev"); res != "ok" || !now {
		t.Fatalf("grant: res=%q now=%v, want ok/true", res, now)
	}
	if !p.CanType {
		t.Error("CanType not set after grant")
	}
	if _, now, _ := s.GrantToggle("dev"); now {
		t.Error("second grant should toggle typing back off")
	}
}

func TestGrantToggle_NotFound(t *testing.T) {
	s := newTestSession(false)
	if _, _, res := s.GrantToggle("ghost"); res != "notfound" {
		t.Errorf("result = %q, want notfound", res)
	}
}

// Opening the line lets full-access guests type but leaves viewers watch-only.
func TestToggleGuests_ViewersStayWatchOnly(t *testing.T) {
	s := newTestSession(false)
	viewer := attach(s, "v", false, false)
	full := attach(s, "f", false, true)
	if !s.ToggleGuests() {
		t.Fatal("ToggleGuests should report the line is now open")
	}
	if viewer.CanType {
		t.Error("viewer became typeable when the line opened — must stay watch-only")
	}
	if !full.CanType {
		t.Error("full-access guest should type on an open line")
	}
}

func TestToggleLock(t *testing.T) {
	s := newTestSession(false)
	if s.Locked() {
		t.Fatal("new session should not be locked")
	}
	if !s.ToggleLock() || !s.Locked() {
		t.Error("ToggleLock should lock")
	}
	if s.ToggleLock() || s.Locked() {
		t.Error("second ToggleLock should unlock")
	}
}

func TestUniqueName(t *testing.T) {
	s := newTestSession(false)
	a := attach(s, "alice", false, false)
	b := attach(s, "alice", false, false)
	if a.Name != "alice" || b.Name != "alice-2" {
		t.Errorf("names = %q, %q; want alice, alice-2", a.Name, b.Name)
	}
}

// The hard input gate: a viewer's keystrokes must never reach the program (writing
// to s.ptmx). With a nil ptmx, any write would panic — so a clean pass proves the
// gate held. The blocked keystroke should bell the viewer instead.
func TestHandleInput_ViewerKeystrokeBlocked(t *testing.T) {
	s := newTestSession(false)
	p := &Participant{Name: "v", IsHost: false, FullAccess: false, CanType: false, out: make(chan []byte, 4)}
	if !s.HandleInput(p, []byte("x")) {
		t.Fatal("HandleInput should not disconnect on a normal keystroke")
	}
	select {
	case b := <-p.out:
		if len(b) != 1 || b[0] != '\a' {
			t.Errorf("expected a bell to the viewer, got %q", b)
		}
	default:
		t.Error("viewer keystroke produced no bell — gate may have forwarded it")
	}
}

// A viewer scrolling the mouse must NOT bell — mouse reports are escape sequences,
// not typing attempts, so they stay silent (the endless-beep bug).
func TestHandleInput_MouseScrollDoesNotBell(t *testing.T) {
	s := newTestSession(false)
	p := &Participant{Name: "v", IsHost: false, FullAccess: false, CanType: false, out: make(chan []byte, 8)}
	// An SGR mouse-wheel report (what a terminal sends when scrolling).
	for i := 0; i < 5; i++ {
		if !s.HandleInput(p, []byte("\x1b[<64;10;20M")) {
			t.Fatal("HandleInput should not disconnect on a mouse report")
		}
	}
	select {
	case b := <-p.out:
		t.Errorf("mouse scroll belled the viewer (got %q) — should be silent", b)
	default:
	}
	// A real keystroke still bells (once), proving the gate still works.
	if !s.HandleInput(p, []byte("x")) {
		t.Fatal("HandleInput should not disconnect on a keystroke")
	}
	select {
	case b := <-p.out:
		if len(b) != 1 || b[0] != '\a' {
			t.Errorf("expected a bell on a real keystroke, got %q", b)
		}
	default:
		t.Error("a real typing attempt should still bell")
	}
}

// The HUD card labels each participant by role and never leaks an escape sequence
// from a hostile name.
func TestHUDCard(t *testing.T) {
	s := newTestSession(false)
	attach(s, "alice", true, false)         // host
	attach(s, "bob", false, true)           // full-access (closed line → watching)
	attach(s, "ev\x1b[31mil", false, false) // viewer with an escape in the name
	s.mu.Lock()
	card := s.hudLinesLocked()
	s.mu.Unlock()

	for _, want := range []string{"3 on the line", "guests: view-only", "locked: no", "host", "full · watching", "viewer · watching"} {
		if !strings.Contains(card, want) {
			t.Errorf("HUD missing %q in:\n%s", want, card)
		}
	}
	// the raw ESC from the hostile name must not survive into the card
	if strings.Contains(card, "\x1b[31m") {
		t.Error("HUD leaked a participant-supplied escape sequence")
	}
}

func TestSanitizeNotice_StripsEscapes(t *testing.T) {
	in := "al\x1b[31mice\x07\nbob"
	got := sanitizeNotice(in)
	for _, r := range got {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control char survived sanitize: %q in %q", r, got)
		}
	}
	if got != "al[31micebob" {
		t.Errorf("sanitizeNotice = %q", got)
	}
}
