package main

import (
	"fmt"
	"os"
	"strconv"
	"unicode/utf8"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/brand"
)

// The raw-mode readers for the composited modals (menu_modal.go). These replace the
// cgBox-then-fmt.Printf pattern: the list, the input row and the footer are all interior lines of
// ONE painted frame, and a keystroke repaints that frame instead of printing under it.
//
// Key handling follows the mux's own menus (menuKey here, welcomeLoop in llms_welcome.go): raw for
// the read, previous mode restored on EVERY exit path, esc/q/ctrl-c/ctrl-\ always get you out, and
// escape SEQUENCES (arrows) are never mistaken for a lone esc. No screen here can trap you — that
// was a real prior bug in this exact modal.

// cgRaw runs fn with stdin in raw mode and restores the prior (cooked) mode plus a visible cursor on
// every exit path, including a panic. Returns false when stdin isn't a tty: there is nobody to read
// from, and a modal must never block a pipe or the daemon.
func cgRaw(fn func(read func() ([]byte, bool))) bool {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return false
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer func() {
		os.Stdout.WriteString("\x1b[?25h")
		_ = term.Restore(fd, old)
	}()
	buf, pend := make([]byte, 64), []byte(nil)
	fn(func() ([]byte, bool) {
		if len(pend) == 0 {
			n, rerr := os.Stdin.Read(buf)
			if rerr != nil || n == 0 {
				return nil, false
			}
			pend = append(pend[:0], buf[:n]...)
		}
		// ONE keystroke per call. A read can carry several — a fast typist's two digits, or a
		// pasted line — and handing the whole chunk over would act on the first byte and DROP the
		// rest. An escape sequence is the exception: its bytes are one keystroke together.
		if pend[0] == 0x1b && len(pend) > 1 {
			k := pend
			pend = nil
			return k, true
		}
		w := 1
		if pend[0] >= 0x80 { // a multi-byte rune is also ONE keystroke
			_, w = utf8.DecodeRune(pend)
		}
		k := pend[:w]
		pend = pend[w:]
		return k, true
	})
	return true
}

// cgChoice is a letter key a picker offers alongside the numbers ("n  ask someone new").
type cgChoice struct {
	Key   rune
	Label string
}

// cgPicker is a composited numbered picker. Verb says what a number DOES ("open", "pick a peer") —
// it is both the hint label and the input row's prompt, so the two can't drift.
type cgPicker struct {
	Title  string
	Body   []string
	Items  []string
	Verb   string
	Extras []cgChoice
}

// cgIsExit reports whether a keystroke means "get me out": esc (alone), ctrl-c, ctrl-\, or q.
func cgIsExit(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	switch b[0] {
	case 0x1b:
		return len(b) == 1
	case 0x03, 0x1c, 'q', 'Q':
		return true
	}
	return false
}

// cgArrow returns 'A'/'B' for up/down arrow presses, 0 otherwise.
func cgArrow(b []byte) byte {
	if len(b) >= 3 && b[0] == 0x1b && b[1] == '[' && (b[2] == 'A' || b[2] == 'B') {
		return b[2]
	}
	return 0
}

// run paints the picker and reads a choice.
//
//   - a bare digit selects IMMEDIATELY when it addresses a real item — no enter, which is the whole
//     point: picking one of a handful of peers should cost one keystroke
//   - lists longer than 9 items also accept a typed multi-digit number confirmed with enter (a bare
//     '1' can't select there — it might be the start of "12")
//   - an extra key returns (-1, key, true)
//   - esc / q / ctrl-c / ctrl-\ / enter-on-empty / no tty return (-1, 0, false)
//   - ↑↓ scroll when the list is taller than the frame
//
// An out-of-range number selects nothing and does nothing else — it never exits the process, and it
// no longer silently cancels the way the old Pick did on a typo.
func (p cgPicker) run() (idx int, key rune, ok bool) {
	if len(p.Items) == 0 {
		return -1, 0, false
	}
	multi := len(p.Items) > 9
	idx, key, ok = -1, 0, false
	off, typed, shown := 0, "", 0

	hints := []brand.Hint{{Key: cgNumKey(len(p.Items)), Label: p.Verb}}
	for _, e := range p.Extras {
		hints = append(hints, brand.Hint{Key: string(e.Key), Label: e.Label})
	}
	hints = append(hints, brand.Hint{Key: "q · esc", Label: "back to your session"})

	cgRaw(func(read func() ([]byte, bool)) {
		for {
			m := cgModal{Title: p.Title, Body: p.Body, Items: p.Items, Hints: hints, Off: off,
				Prompt: cgPromptRow(p.Verb, cgNumKey(len(p.Items)), typed)}
			shown = m.paint()

			b, got := read()
			if !got || cgIsExit(b) {
				return
			}
			if a := cgArrow(b); a != 0 {
				if a == 'A' && off > 0 {
					off--
				}
				if a == 'B' && off+shown < len(p.Items) {
					off++
				}
				continue
			}
			c := rune(b[0])
			switch {
			case c == '\r' || c == '\n':
				if !multi || typed == "" {
					return // enter closes — same contract as every other ctrl-\ screen
				}
				n, err := strconv.Atoi(typed)
				if err != nil || n < 1 || n > len(p.Items) {
					typed = "" // out of range: nothing selected, nothing crashed, try again
					continue
				}
				idx, ok = n-1, true
				return
			case c == 0x7f || c == 0x08:
				if len(typed) > 0 {
					typed = typed[:len(typed)-1]
				}
			case c >= '0' && c <= '9':
				if multi {
					if len(typed) < len(strconv.Itoa(len(p.Items))) {
						typed += string(c)
					}
					continue
				}
				n := int(c - '0')
				if n >= 1 && n <= len(p.Items) {
					idx, ok = n-1, true
					return
				}
			default:
				for _, e := range p.Extras {
					if c == e.Key || c == e.Key-('a'-'A') {
						key, ok = e.Key, true
						return
					}
				}
			}
		}
	})
	return idx, key, ok
}

// cgNumKey is how the number keys are advertised: "1-9" when every item is one keystroke away,
// "1-24 ⏎" when they aren't, because there the enter is not optional.
func cgNumKey(n int) string {
	if n > 9 {
		return fmt.Sprintf("1-%d ⏎", n)
	}
	return fmt.Sprintf("1-%d", n)
}

// cgPromptRow is the input row that lives INSIDE the frame, in the established `› ` idiom. The
// cursor lands at the end of it (menu_modal.go computes that), so typed text echoes in the box.
func cgPromptRow(label, hint, typed string) string {
	row := "  " + label
	if hint != "" {
		row += " " + dim("("+hint+")")
	}
	return row + " " + sgr(cgKey, "›") + " " + typed
}
