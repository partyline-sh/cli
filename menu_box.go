package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/brand"
)

// The cooked-mode ctrl-\ menus (context threads, worktrees) render their screens through
// cgBox so they read as centered rounded modals — the same look as the mux's exit-confirm box
// (internal/ptymux drawCenteredBox), which we can't call from here (different package) so we
// replicate its geometry. Unlike that box, we KEEP the cursor visible and park it below the
// frame: these menus take cooked-mode line input, and the typed text echoes at the cursor.
//
// Width and clipping come from internal/brand — this file used to carry its own cgVisLen /
// cgClip / cgFit, which is how the borders here and the mux's could disagree by a column.

// menuKey reads ONE keypress for a boxed menu in raw mode, so a menu acts on a single
// keystroke (no enter needed) and Esc cancels. Returns the lowercased rune for a normal
// key, '\n' for enter, or 0 for CANCEL (a lone Esc, Ctrl-C, or Ctrl-\). Escape SEQUENCES
// (arrow / function keys — ESC followed by more bytes) are swallowed and it reads again,
// so a stray arrow press never cancels. It flips the tty to raw only for the read and
// restores the prior (cooked) mode before returning, so a follow-up text prompt still
// works. Returns 0 immediately when stdin isn't a tty (piped) — nothing to choose.
func menuKey() rune {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return 0
	}
	defer func() { _ = term.Restore(fd, old) }()
	var buf [8]byte
	for {
		n, err := os.Stdin.Read(buf[:])
		if err != nil || n == 0 {
			return 0
		}
		switch buf[0] {
		case 0x1b: // ESC
			if n == 1 {
				return 0 // lone Esc → cancel
			}
			continue // ESC + more bytes = arrow/function key → ignore, read again
		case 0x03, 0x1c: // Ctrl-C / Ctrl-\
			return 0
		case '\r', '\n':
			return '\n'
		}
		r := rune(buf[0])
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A' // fold case so 'Q' == 'q'
		}
		return r
	}
}

// cgExitHints is what every ctrl-\ menu's way out ACTUALLY is: menuKey() folds esc, ctrl-C and
// ctrl-\ into the same CANCEL as q, and enter into the same close. Every one of these menus used
// to end with a lone `q  back to your session` row, so three of the four exits went undocumented
// on eight screens — people pressed esc, it worked, and nothing had told them it would.
func cgExitHints() []brand.Hint {
	return []brand.Hint{{Key: "q · esc · ⏎", Label: "back to your session"}}
}

// cgHintRow is the closing hint bar for a cgBox menu (returned as a box line). Menus whose exit
// does something extra (mcp's apply-on-close, recover's back-to-the-switchboard) call
// brand.HintBar directly so the label tells the truth rather than reusing this one.
func cgHintRow(mode string) string {
	return "  " + brand.HintBar(mode, cgExitHints(), 0)
}

// cgHintPrint is cgHintRow for the menus that print rather than box (cgFrame + cgItem screens).
func cgHintPrint(mode string) { fmt.Println(cgHintRow(mode)) }

// cgRow formats one aligned action row for a boxed menu — colored key, label, optional dim
// note — and RETURNS it (cgBox draws a slice of pre-styled lines). Mirrors cgItem's layout.
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

// cgBox clears the screen and draws a centered rounded box (╭─╮ │ ╰─╯) of pre-styled lines,
// with a title row on top, then parks the cursor two rows below the frame so the caller's
// cooked-mode `› ` prompt and the user's echoed input sit just under the modal. Terminal size
// comes from x/term (we're in cooked mode here); it falls back to 80×24. Long lines are clipped.
func cgBox(title string, lines []string) {
	cols, rows := 80, 24
	if c, r, err := term.GetSize(int(os.Stdout.Fd())); err == nil && c > 0 && r > 0 {
		cols, rows = c, r
	}

	all := make([]string, 0, len(lines)+2)
	all = append(all, cgBold+"☎  "+title+cgOff, "")
	all = append(all, lines...)

	maxW := cols - 4
	if maxW < 8 {
		maxW = 8
	}
	for i, l := range all {
		all[i] = brand.ClipEllipsis(l, maxW)
	}

	w := 0
	for _, l := range all {
		if v := brand.VisWidth(l); v > w {
			w = v
		}
	}
	top := (rows - (len(all) + 2)) / 2
	if top < 1 {
		top = 1
	}
	left := (cols - (w + 4)) / 2
	if left < 1 {
		left = 1
	}

	clr := "\x1b[38;5;215m" // same warm border color as the mux's drawCenteredBox
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[?25h") // clear + KEEP the cursor visible for the cooked input
	fmt.Fprintf(&b, "\x1b[%d;%dH%s╭%s╮\x1b[0m", top, left, clr, strings.Repeat("─", w+2))
	for i, l := range all {
		pad := w - brand.VisWidth(l)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH%s│\x1b[0m %s%s %s│\x1b[0m", top+1+i, left, clr, l, strings.Repeat(" ", pad), clr)
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH%s╰%s╯\x1b[0m", top+1+len(all), left, clr, strings.Repeat("─", w+2))

	// Park the cursor below the frame, aligned with the box's left edge, for the prompt.
	promptRow := top + len(all) + 3
	if promptRow > rows {
		promptRow = rows
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH", promptRow, left)
	os.Stdout.WriteString(b.String())
}
