package ptysess

import (
	"bytes"
	"testing"
)

// A bare ctrl-\ from the host must open the command menu and paint it.
func TestHostBarePrefixOpensMenu(t *testing.T) {
	var term bytes.Buffer
	gw := &gateWriter{dst: &term}
	host := &Participant{ID: 1, Name: "host", IsHost: true, FullAccess: true, CanType: true, Cols: 80, Rows: 24}
	s := &Session{hostGate: gw, parts: map[int64]*Participant{1: host}}

	if !s.HandleInput(host, []byte{PrefixKey}) {
		t.Fatal("HandleInput returned false (treated as leave)")
	}
	if !s.hostMenu {
		t.Fatal("hostMenu flag not set — menu did not open")
	}
	out := term.String()
	if len(out) == 0 {
		t.Fatal("nothing painted to the host terminal")
	}
	if !bytes.Contains([]byte(out), []byte("who's on the line")) {
		t.Fatalf("menu items not rendered; got %q", out)
	}
}
