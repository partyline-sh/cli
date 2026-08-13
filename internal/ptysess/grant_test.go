package ptysess

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

// The gate buffer used to be unbounded: a chatty program behind a modal nobody answered could
// hold arbitrarily much memory. It's now a ring — never larger than the cap, oldest bytes dropped.
func TestGateBufferNeverExceedsItsCap(t *testing.T) {
	var term bytes.Buffer
	gw := &gateWriter{dst: &term}
	gw.openGate()

	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < 40; i++ { // 2.5 MB through a 256 KB gate
		if _, err := gw.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if got := gw.buf.Len(); got > gateMaxBuf {
			t.Fatalf("buffer grew to %d bytes, cap is %d", got, gateMaxBuf)
		}
	}
	if gw.droppedBytes() == 0 {
		t.Fatal("nothing recorded as dropped after overflowing the cap")
	}
	// A single write larger than the whole cap is also bounded.
	gw.openGate()
	if _, err := gw.Write(bytes.Repeat([]byte("y"), gateMaxBuf*3)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := gw.buf.Len(); got > gateMaxBuf {
		t.Fatalf("one oversized write left %d bytes buffered, cap is %d", got, gateMaxBuf)
	}
}

// While the gate is CLOSED nothing is buffered — writes go straight to the terminal.
func TestGateWritesThroughWhenClosed(t *testing.T) {
	var term bytes.Buffer
	gw := &gateWriter{dst: &term}
	if _, err := gw.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if term.String() != "hello" {
		t.Fatalf("closed gate wrote %q to the terminal, want %q", term.String(), "hello")
	}
	if gw.buf.Len() != 0 {
		t.Fatal("closed gate buffered instead of writing through")
	}
}

// The sticky modal must time out, and it must time out to DECLINE — an unanswered permission
// prompt can only safely mean "no". Auto-granting on a walk-away would be a security bug.
func TestGrantTimeoutDeclinesAndFreesTheKeyboard(t *testing.T) {
	old := grantAnswerTimeout
	grantAnswerTimeout = 20 * time.Millisecond
	defer func() { grantAnswerTimeout = old }()

	var term bytes.Buffer
	gw := &gateWriter{dst: &term}
	host := &Participant{ID: 1, Name: "host", IsHost: true, FullAccess: true, CanType: true, Cols: 80, Rows: 24, out: make(chan []byte, 8)}
	guest := &Participant{ID: 2, Name: "guest", FullAccess: true, Cols: 80, Rows: 24, out: make(chan []byte, 8)}
	s := &Session{
		hostGate: gw,
		vt:       vt.NewSafeEmulator(80, 24),
		parts:    map[int64]*Participant{1: host, 2: guest},
	}

	s.openGrantModal(guest)
	s.mu.Lock()
	granting := s.granting
	s.mu.Unlock()
	if !granting {
		t.Fatal("modal did not open")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		granting, pending := s.granting, s.pendingReq
		s.mu.Unlock()
		if !granting && pending == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("modal never expired — the host's keyboard would stay locked")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Declined, not granted: the guest must not have been given typing.
	if guest.CanType {
		t.Fatal("timeout GRANTED control — it must always decline")
	}
	if !drainContains(guest.out, "kept control") {
		t.Error("the requester was not told the host kept control")
	}
}

// An answered request must not be re-resolved by its own expiry goroutine firing later.
func TestGrantExpiryIgnoresAnAlreadyAnsweredRequest(t *testing.T) {
	var term bytes.Buffer
	gw := &gateWriter{dst: &term}
	host := &Participant{ID: 1, Name: "host", IsHost: true, FullAccess: true, CanType: true, Cols: 80, Rows: 24, out: make(chan []byte, 8)}
	guest := &Participant{ID: 2, Name: "guest", FullAccess: true, Cols: 80, Rows: 24, out: make(chan []byte, 8)}
	s := &Session{hostGate: gw, vt: vt.NewSafeEmulator(80, 24), parts: map[int64]*Participant{1: host, 2: guest}}

	s.granting, s.pendingReq = true, guest
	gw.openGate()
	s.resolveGrant(false) // host answered
	drainContains(guest.out, "")

	old := grantAnswerTimeout
	grantAnswerTimeout = time.Millisecond
	defer func() { grantAnswerTimeout = old }()
	s.expireGrant(guest) // the stale timer fires: must be a no-op

	if drainContains(guest.out, "kept control") {
		t.Error("a stale expiry re-notified the requester")
	}
}

// drainContains empties a participant's out channel and reports whether any frame held want.
func drainContains(ch chan []byte, want string) bool {
	found := false
	for {
		select {
		case b := <-ch:
			if want != "" && strings.Contains(string(b), want) {
				found = true
			}
		default:
			return found
		}
	}
}
