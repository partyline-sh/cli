package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The compose field's reader and its $EDITOR hand-off. Split from menu_compose.go so neither file has
// to hold both the layout and the key contract.

// cgCompose reads a multi-line message. ok=false means ABANDONED, kept distinct from an empty message
// exactly as cgAsk does, so a caller can unwind a chain of prompts. `initial` seeds the field (a retry
// after the server refused something keeps your words).
//
// The loop exists for $EDITOR: a child process needs the tty in the mode it was found in, so the only
// correct place to spawn it is OUTSIDE cgRaw. Each pass is one raw session; ctrl-e ends the session,
// runs the editor cooked, and re-enters with whatever came back.
func cgCompose(title string, body []string, initial string) (string, bool) {
	text, note := initial, ""
	for {
		out, res := cgComposeRead(title, body, text, note)
		text, note = out, ""
		switch res {
		case cgComposeSend:
			return text, true
		case cgComposeEditor:
			text, note = cgEditorText(text)
		default:
			return "", false
		}
	}
}

// cgComposeRead is ONE raw session on the field. Returns the text as it stands and why the session
// ended. Bracketed paste is enabled for the field only and disabled on every exit path — leaving DECSET
// 2004 set would hand \x1b[200~ to the next reader, which has no idea what that is.
func cgComposeRead(title string, body []string, text, note string) (string, cgComposeOutcome) {
	out, res := text, cgComposeAbandon
	if !cgRaw(func(read func() ([]byte, bool)) {
		cgPasteMode(true)
		defer cgPasteMode(false)
		// Bytes handed back by the paste assembler (whatever followed the terminator) are KEYSTROKES and
		// must be seen by the loop — dropping them would eat a keypress.
		var pend [][]byte
		next := func() ([]byte, bool) {
			if len(pend) > 0 {
				b := pend[0]
				pend = pend[1:]
				return b, true
			}
			return read()
		}
		for {
			cgComposePaint(title, body, out, note)
			b, got := next()
			if !got || cgIsExit1(b) {
				return
			}
			if p, isPaste := cgPasteBegins(b); isPaste {
				txt, rest, complete := cgReadPaste(p, next, time.Now)
				out += cgNormalizeNewlines(txt)
				note = ""
				if !complete {
					note = "that paste arrived with no end marker — check the tail before you send"
				}
				if len(rest) > 0 {
					pend = append(pend, rest)
				}
				continue
			}
			switch b[0] {
			case 0x04: // ctrl-d — SEND. The bound is checked HERE, not by the server: composing 40,000
				// characters and learning from a 400 that 8,000 was the limit is the worst possible order.
				if over := questionTooLongNote(out); over != "" {
					note = over
					continue
				}
				if strings.TrimSpace(out) == "" {
					note = "nothing to send yet — type or paste your question, then ctrl-d"
					continue
				}
				res = cgComposeSend
				return
			case 0x05: // ctrl-e — hand off to $EDITOR (outside raw mode; see cgCompose)
				res = cgComposeEditor
				return
			case '\r', '\n': // a NEWLINE, not a submit. The whole reason this field exists.
				out += "\n"
			case 0x15: // ctrl-u — clear
				out, note = "", ""
			case 0x7f, 0x08:
				if r := []rune(out); len(r) > 0 {
					out = string(r[:len(r)-1])
				}
			case 0x1b: // an escape SEQUENCE (arrow, function key) is not text — a lone esc already left
			default:
				out += cgPrintable(b)
			}
		}
	}) {
		return text, cgComposeAbandon // no tty: nothing to read from, and a modal must never block a pipe
	}
	return out, res
}

// cgPrintable keeps the typeable characters out of a keystroke chunk. A chunk can carry several bytes
// (a fast typist, or a paste from a terminal that doesn't do bracketed paste), so this appends all of
// them — but never a control character, which would corrupt the frame and travel to the peer invisibly.
func cgPrintable(b []byte) string {
	var out []rune
	for _, r := range string(b) {
		if r >= 0x20 && r != 0x7f {
			out = append(out, r)
		}
	}
	return string(out)
}

// cgNormalizeNewlines folds CRLF and lone CR into LF. This is the ONE thing a paste is not taken
// byte-for-byte on, and it has to be: a CR in the buffer paints the rest of the line over the frame's
// left border, and \r\n vs \n is not a difference in what the text SAYS. Everything else — tabs, unicode,
// trailing whitespace, blank lines — survives exactly as pasted.
func cgNormalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// cgEditorText opens the text in $EDITOR and reads it back. Returns the text (unchanged on any failure —
// losing a composed question to a missing editor would be unforgivable) and a note for the field.
//
// The child gets the real stdin/stdout/stderr in the mode cgRaw restored, which is why this runs between
// raw sessions and not inside one: a full-screen editor on a raw tty with no echo is unusable.
func cgEditorText(text string) (string, string) {
	ed := strings.TrimSpace(os.Getenv("EDITOR"))
	if ed == "" {
		return text, "$EDITOR isn't set, so there's nothing to open — `export EDITOR=nano` (or vim, or `code -w`) and press ctrl-e again"
	}
	dir, err := os.MkdirTemp("", "ptln-question")
	if err != nil {
		return text, "couldn't make a scratch file for the editor: " + err.Error()
	}
	defer os.RemoveAll(dir)
	// .md so an editor's syntax highlighting and soft-wrap do something sensible with prose.
	path := filepath.Join(dir, "question.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return text, "couldn't write the scratch file: " + err.Error()
	}
	// sh -c so $EDITOR may carry flags ("code -w", "emacsclient -nw") the way every other tool honours it.
	cmd := exec.Command("sh", "-c", ed+` "$1"`, "sh", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return text, "$EDITOR (" + ed + ") exited badly: " + err.Error() + " — your text is untouched"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return text, "couldn't read the editor's file back: " + err.Error() + " — your text is untouched"
	}
	return strings.TrimRight(cgNormalizeNewlines(string(b)), "\n"), ""
}
