package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// The cooked-mode ctrl-\ menus (context threads, worktrees) render their screens through
// cgBox so they read as centered rounded modals — the same look as the mux's exit-confirm box
// (internal/ptymux drawCenteredBox), which we can't call from here (different package) so we
// replicate its geometry. Unlike that box, we KEEP the cursor visible and park it below the
// frame: these menus take cooked-mode line input, and the typed text echoes at the cursor.

// cgVisLen counts the visible terminal columns of s, skipping ANSI SGR escapes and measuring
// each rune by display width — the same rule the mux's visLen uses, so our borders line up.
func cgVisLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		n += runewidth.RuneWidth(r)
		i += size
	}
	return n
}

// cgClip truncates s to at most max visible columns, preserving ANSI escapes verbatim and
// appending an ellipsis + reset when it has to cut. Pure — the box's width-fit is unit-tested.
func cgClip(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if cgVisLen(s) <= max {
		return s
	}
	var b strings.Builder
	limit := max - 1 // leave a column for the ellipsis
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if w+rw > limit {
			break
		}
		b.WriteString(s[i : i+size])
		w += rw
		i += size
	}
	b.WriteString("…\x1b[0m")
	return b.String()
}

// cgFit clips every line to max visible columns (see cgClip). Pure.
func cgFit(lines []string, max int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = cgClip(l, max)
	}
	return out
}

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
	all = cgFit(all, maxW)

	w := 0
	for _, l := range all {
		if v := cgVisLen(l); v > w {
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
		pad := w - cgVisLen(l)
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
