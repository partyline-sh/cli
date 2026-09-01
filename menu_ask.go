package main

import (
	"strings"

	"partyline.sh/partyline/internal/brand"
)

// The other three composited readers: a text field, a yes/no, and a dismissable note. Same rule as
// the picker — the input row is an interior line of the frame and a keystroke repaints the frame,
// so nothing is ever printed after the paint (see menu_modal.go for why that mattered).

// cgAsk reads one line with the field INSIDE the frame. ok=false means CANCELLED, kept distinct from
// an empty answer so callers can unwind a chain of prompts (prompt.go's rule, unchanged).
//
// Escapes: esc / ctrl-c / ctrl-\ cancel at once; a bare `q` cancels on enter (Input's rule — on a
// free-text field q has to be typeable, so it can only mean cancel when it's the whole answer);
// enter on an empty field takes def, or cancels when there is no default.
func cgAsk(title string, body []string, label, def string) (string, bool) {
	hint := "esc cancels"
	if def != "" {
		hint = "enter for " + def + " · esc cancels"
	}
	typed, out, ok := []rune(nil), "", false
	cgRaw(func(read func() ([]byte, bool)) {
		for {
			m := cgModal{Title: title, Body: body,
				Prompt: cgPromptRow(label, hint, string(typed)),
				Hints:  []brand.Hint{{Key: "⏎", Label: "ok"}, {Key: "esc", Label: "back to your session"}}}
			m.paint()

			b, got := read()
			if !got || cgIsExit1(b) {
				return
			}
			if b[0] == 0x1b { // an escape SEQUENCE (arrow, function key) is not text
				continue
			}
			switch b[0] {
			case '\r', '\n':
				s := strings.TrimSpace(string(typed))
				switch {
				case strings.EqualFold(s, "q"):
					return
				case s == "":
					if def == "" {
						return
					}
					out, ok = def, true
				default:
					out, ok = s, true
				}
				return
			case 0x7f, 0x08:
				if len(typed) > 0 {
					typed = typed[:len(typed)-1]
				}
			case 0x15: // ctrl-u — clear the field
				typed = nil
			default:
				if b[0] >= 0x20 {
					typed = append(typed, []rune(string(b))...)
				}
			}
		}
	})
	return out, ok
}

// cgIsExit1 is cgIsExit without the `q` rule, for the screens where q is a character you can type.
func cgIsExit1(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	switch b[0] {
	case 0x1b:
		return len(b) == 1
	case 0x03, 0x1c:
		return true
	}
	return false
}

// cgConfirm is Confirm inside the frame: it names all three outcomes — [y] [n] [esc] — because that
// is the one form that leaves no doubt there is a way out. ok=false means cancelled; a deliberate no
// is (false, true).
func cgConfirm(title string, body []string, label string, def bool) (val bool, ok bool) {
	y, n := "y", "n"
	if def {
		y = "Y"
	} else {
		n = "N"
	}
	cgRaw(func(read func() ([]byte, bool)) {
		for {
			m := cgModal{Title: title, Body: body,
				Prompt: cgPromptRow(label, y+"/"+n+"/esc", ""),
				Hints: []brand.Hint{{Key: "y", Label: "yes"}, {Key: "n", Label: "no"},
					{Key: "q · esc", Label: "back to your session"}}}
			m.paint()

			b, got := read()
			if !got || cgIsExit(b) {
				return
			}
			switch b[0] {
			case '\r', '\n':
				val, ok = def, true
				return
			case 'y', 'Y':
				val, ok = true, true
				return
			case 'n', 'N':
				val, ok = false, true
				return
			}
		}
	})
	return val, ok
}

// cgNote is the composited message box: the dismiss row lives inside the frame and the keypress is
// read raw, so — unlike cgBox+pause — nothing prints below the box at whatever column the cursor
// happened to be parked at. Any key dismisses it; with no tty it returns at once.
func cgNote(title string, lines []string) {
	m := cgModal{Title: title, Body: lines,
		Hints: []brand.Hint{{Key: "⏎ · esc", Label: "back"}}}
	m.paint() // painted before raw mode, so a headless run still leaves the frame on screen
	cgRaw(func(read func() ([]byte, bool)) { read() })
}
