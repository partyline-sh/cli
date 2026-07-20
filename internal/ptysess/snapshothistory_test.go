package ptysess

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// SnapshotHistory replays a session's scrollback + current screen so the terminal's NATIVE
// buffer holds this session's history. The invariant that #238 broke: with the mux reserving
// the bottom row for its status bar, the child screen is (termRows-1) tall, but the replay
// runs on the full termRows-tall terminal. When scrollback is present, a stray history line
// used to cling to row 1, floating the screen down onto the bar row and putting the cursor one
// row too high. Padding the screen block to the full viewport (viewRows) fixes it.
//
// We verify headlessly: render the replay into a fresh full-height emulator (the real terminal)
// and assert the child screen lands at the TOP rows and the cursor matches — for a range of
// scrollback depths, including the ones that used to trigger the off-by-one.
func TestSnapshotHistoryLandsAtTop(t *testing.T) {
	const cols, termRows = 40, 10
	const childRows = termRows - 1 // the mux reserves the bottom row for its status bar

	for _, tc := range []struct {
		name  string
		lines int
	}{
		{"no scrollback", 3},
		{"just fills child screen", childRows},
		{"one line of scrollback (the classic off-by-one trigger)", termRows},
		{"scrollback present", 25},
		{"large scrollback", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{vt: vt.NewSafeEmulator(cols, childRows)}
			for i := 0; i < tc.lines; i++ {
				fmt.Fprintf(s.vt, "line %d\r\n", i)
			}
			_, _ = s.vt.Write([]byte("prompt> ")) // end mid-line: non-zero cursor column too

			wantChild := strings.Split(s.vt.Render(), "\n") // childRows rows
			wantPos := s.vt.CursorPosition()

			replay := s.SnapshotHistory(1500, termRows)

			term := vt.NewSafeEmulator(cols, termRows) // the real terminal is full height
			_, _ = term.Write(replay)
			got := strings.Split(term.Render(), "\n")

			// The child screen must occupy the TOP childRows rows (row `termRows` is the bar row).
			for r := 0; r < childRows; r++ {
				if got[r] != wantChild[r] {
					t.Errorf("row %d mismatch:\n want %q\n  got %q", r, wantChild[r], got[r])
				}
			}
			// Cursor is child-relative; since the screen starts at row 1, it must match exactly.
			gotPos := term.CursorPosition()
			if gotPos.X != wantPos.X || gotPos.Y != wantPos.Y {
				t.Errorf("cursor off: want (col=%d,row=%d) got (col=%d,row=%d)", wantPos.X, wantPos.Y, gotPos.X, gotPos.Y)
			}
		})
	}
}
