package brand

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The hint bar is the standard footer for EVERY surface that reads a keypress: a mode pill
// naming where you are, then only the keys that are actually live there.
//
// The contract for adopters: the hint list must be DERIVED from the same predicate the key
// dispatcher uses (see ptymux.drawCmdPanel, which gates each listed command on the same nil
// check the dispatcher gates the action on). A hand-maintained parallel list rots — that is how
// `esc` came to be undocumented on eight menus that all handled it.
type Hint struct {
	Key   string // as shown: "↵", "esc", "←/→", "1-9". Empty = a plain note, no key column.
	Label string
}

const (
	hintKeyClr = "\x1b[1;38;5;215m"
	hintLblClr = "\x1b[38;5;245m"
	hintSepClr = "\x1b[38;5;240m"
)

// HintBar renders the pill + hints as one row. width caps the total display columns (0 = no
// cap); hints are dropped from the RIGHT when they don't fit, so the pill — the one part that
// tells you where you are — always survives. Order the hints most-important-first.
//
// Always exactly one line: no caller may add a newline, and the mux's bottom bar depends on it
// (see drawBar — a second row breaks the rows-1 arithmetic the child screens are sized by).
func HintBar(mode string, hints []Hint, width int) string {
	bar := " "
	if mode != "" {
		bar += Pill(mode)
	}
	barW := VisWidth(bar)
	sep := " " + hintSepClr + "·" + reset + " "
	for i, h := range hints {
		lead := " "
		if i > 0 {
			lead = sep
		}
		next := lead + hintLblClr + h.Label + reset
		if h.Key != "" {
			next = lead + hintKeyClr + ShiftKey(h.Key) + reset + " " + hintLblClr + h.Label + reset
		}
		nw := VisWidth(next)
		if width > 0 && barW+nw > width {
			break
		}
		bar += next
		barW += nw
	}
	return bar
}

// PickerHints is the hint set for a vertical ↑↓ list picker: the shape shared by the in-session
// host menu and the joiner palette, which each had their own hand-written copy of this row and
// each named only two of the four ways out. The EXITS come before the letter-jump nicety
// deliberately — HintBar drops from the right, and "how do I get out of this" is the hint you
// must never lose.
func PickerHints() []Hint {
	return []Hint{
		{Key: "↑↓ jk", Label: "move"},
		{Key: "⏎", Label: "select"},
		{Key: "esc q", Label: "close"},
		{Key: "a-z", Label: "jump"},
	}
}

// IndentedHintBar renders a hint bar as the footer of a CENTERED overlay (the host menu, the
// joiner palette, the grant modal). It indents to line up with the frame above, but gives that
// up rather than truncate: an overlay footer cannot wrap onto a second row without painting
// over the frame, so the choice is "less indented" or "loses its exits", and it takes the former.
func IndentedHintBar(mode string, hints []Hint, indent, width int) string {
	bar := HintBar(mode, hints, width)
	if slack := width - VisWidth(bar); indent > slack {
		indent = slack
	}
	if indent < 0 {
		indent = 0
	}
	return strings.Repeat(" ", indent) + bar
}

// ShiftKey renders a hotkey as the keys you actually press. A lone capital letter means shift is
// part of the chord, and printing a bare "S" beside a lowercase "s" bound to something ELSE — as
// the board does, where s is jump-to-session and S is switch-board — is a bar that lies about the
// keyboard. Anyone reading "S" reasonably tries s and gets the other command.
//
// Only a single letter: "PR" is a word, and "1-9" is a range.
func ShiftKey(k string) string {
	if utf8.RuneCountInString(k) != 1 {
		return k
	}
	r, _ := utf8.DecodeRuneInString(k)
	if unicode.IsUpper(r) {
		return "⇧" + k
	}
	return k
}
