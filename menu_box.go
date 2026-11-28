package main

import (
	"fmt"
	"os"
	"strings"
)

// The ctrl-\ menus render as centered rounded modals — the same look as the mux's exit-confirm box
// (internal/ptymux drawCenteredBox), which we can't call from here (different package) so we
// replicate its geometry. Width and clipping come from internal/brand — this file used to carry its
// own cgVisLen / cgClip / cgFit, which is how the borders here and the mux's could disagree by a
// column.
//
// Recover and the empty state retain cgBox only for their headless output.
// New screens are popup forms (popup_form.go): cgBox paints a frame and then parks the cursor for
// someone else to print at, which is only sound for exactly one following line.

// cgRow formats one aligned action row for a boxed menu — colored key, label, optional dim
// note — and returns it for cgBox to draw.
func cgRow(key, label, note string) string {
	if note == "" {
		return fmt.Sprintf("    %s%s%s  %s", cgKey, key, cgOff, label)
	}
	pad := 26 - len(label)
	if pad < 2 {
		pad = 2
	}
	return fmt.Sprintf("    %s%s%s  %s%s%s%s%s", cgKey, key, cgOff, label, strings.Repeat(" ", pad), cgDim, note, cgOff)
}

// cgBox draws a centered rounded box of pre-styled lines with a title row, and parks the cursor two
// rows below the frame — see the note above for who may still use that, and why nothing new should.
// The geometry is cgPaintLines' (menu_modal.go): ONE implementation of the frame, so cgBox's border
// and a composited modal's can't drift apart.
func cgBox(title string, lines []string) {
	cols, rows := cgTermSize()
	all := make([]string, 0, len(lines)+2)
	all = append(all, cgBold+"☎  "+title+cgOff, "")
	all = append(all, lines...)
	os.Stdout.WriteString(cgPaintLines(all, cgCursorPark, 0, cols, rows))
}
