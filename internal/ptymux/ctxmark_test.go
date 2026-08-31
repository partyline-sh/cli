package ptymux

import "testing"

// The ☎ marker now carries three overlapping facts — context, agent activity, and a partyline tool
// call. Precedence is the whole design, so it is asserted rather than eyeballed: a PTY cannot be
// stood up in a test, and every previous attempt to verify this row by looking at a terminal missed
// what was actually wrong.
func TestCtxMarkColorPrecedence(t *testing.T) {
	const (
		marked = 51  // a partyline tool call, right now
		green  = 46  // the agent is working
		amber  = 214 // your move
		cyan   = 39  // wired, at rest
		dim    = 245 // record-only, at rest
	)
	for _, tc := range []struct {
		name     string
		ctx      int
		state    string
		stateClr int
		toolLive bool
		want     int
	}{
		{"tool call outranks working", 1, "active", green, true, marked},
		{"tool call outranks your-move", 1, "waiting", amber, true, marked},
		{"tool call lights a record-only session too", 2, "idle", 245, true, marked},
		{"working, no tool call", 1, "active", green, false, green},
		{"your move, no tool call", 1, "waiting", amber, false, amber},
		{"wired and idle", 1, "idle", 245, false, cyan},
		{"record-only and idle", 2, "idle", 245, false, dim},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ctxMarkColor(tc.ctx, tc.state, tc.stateClr, tc.toolLive); got != tc.want {
				t.Errorf("ctxMarkColor(ctx=%d, %q, clr=%d, tool=%v) = %d, want %d",
					tc.ctx, tc.state, tc.stateClr, tc.toolLive, got, tc.want)
			}
		})
	}
}

// The marker is a COLOUR swap, never an extra glyph or a wider segment. SGR is zero-width, so the
// tab strip's layout is identical lit or dark — the property that makes this safe to paint into a
// row the mux shares with a child's frame.
func TestCtxMarkIsColourOnlyAndCannotWidenTheTab(t *testing.T) {
	if a, b := ctxMarkColor(1, "idle", 245, false), ctxMarkColor(1, "idle", 245, true); a == b {
		t.Fatal("a live tool call does not change the colour at all")
	}
	// Both are plain ANSI-256 indexes; neither can encode a glyph or a width.
	for _, c := range []int{ctxMarkColor(1, "idle", 245, true), ctxMarkColor(1, "active", 46, false)} {
		if c < 0 || c > 255 {
			t.Errorf("colour %d is not a valid ANSI-256 index", c)
		}
	}
}
