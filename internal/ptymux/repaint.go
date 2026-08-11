package ptymux

import "fmt"

// repaint.go — the ORDER a tab-switch repaint is emitted in.
//
// A repaint is four things: the child's snapshot (its scrollback + screen), the modes it asked
// for, the scroll region that pins our status bar, and the child's real cursor. Three of those
// are independent. The ORDER of the other two is load-bearing, and getting it wrong is bug #238
// in both directions:
//
//   - The cursor MUST come after the region. DECSTBM homes the cursor, so a region emitted after
//     the snapshot leaves the cursor at 1,1 and every relative move the child makes afterwards is
//     offset — the garbled input line.
//   - The snapshot MUST NOT come after the region. SnapshotHistory pads the screen block to the
//     FULL terminal height (mx.rows) precisely so it lands at row 1 with the bar row free; writing
//     that full-height block INSIDE a bodyRows-tall region makes the region scroll, and the screen
//     no longer lands where the cursor was told to go.
//
// Those two were reconciled by putting the region FIRST, which satisfies the first rule by
// violating the second. The requirement is only that the cursor follows the region — not that the
// region precedes the snapshot. So the region sits between them.
//
// composeRepaint is a pure function over those pieces so the ordering can be asserted in a vt
// round-trip test (repaint_test.go) rather than re-litigated from a screenshot; the last two
// attempts at #238 both shipped on reasoning that looked correct and fixed the wrong thing.
func composeRepaint(snapshot, modes []byte, bodyRows, col, row int) []byte {
	out := make([]byte, 0, len(snapshot)+len(modes)+32)
	out = append(out, snapshot...)
	out = append(out, modes...)
	// A cursor we don't have a real position for gets no region either: asserting the region
	// without repositioning afterwards would home the cursor and cause the very bug above.
	if bodyRows > 0 && col > 0 && row > 0 {
		out = append(out, scrollRegionFor(bodyRows)...)
		out = append(out, []byte(fmt.Sprintf("\x1b[%d;%dH", row, col))...)
	}
	return out
}
