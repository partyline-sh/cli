package main

// The room's input area: status lines, a hint line, and the message being written.
//
// It draws its own rows instead of relying on the terminal to wrap: the text is cut into rows of
// the window's width, the area remembers which row the cursor is on, and a redraw moves to the top
// of the area and clears down. The first version let the terminal wrap and cleared one row, so long
// input left copies of itself on screen with every key.
//
// Keys are decoded in chat_keys.go and edit the buffer in chat_editor.go. Enter sends; Shift-Enter,
// Alt-Enter or "\" then Enter add a line break. Pasted text arrives through bracketed paste as one
// insertion, line breaks included.

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

const (
	chatPrompt        = "you › "
	pasteOn, pasteOff = "\x1b[?2004h", "\x1b[?2004l"
	pasteStart        = "\x1b[200~"
	pasteEnd          = "\x1b[201~"
	chatInputMaxRows  = 12
	chatDoubleEsc     = time.Second
)

type chatInput struct {
	ed      editBuf
	status  []string // one line per session that has something going on
	hint    string   // completion candidates or a short notice, shown above the input until the next key
	pasting bool
	pending []byte // a key sequence split across reads

	// Drawing state: rows the area took, and the cursor's row counted from the area's top.
	rows, curRow int

	history  []string // messages you sent, oldest first
	histIdx  int      // position while browsing; len(history) means "the draft"
	histSave string   // the draft, kept while browsing history

	searching bool
	query     string
	searchAt  int // index in history of the current match, -1 when none

	lastEsc time.Time

	// complete returns completions for the token before the cursor, given the text before it.
	complete func(token, before string) []string
}

// chatInputResult is what a batch of keys asks the room to do.
type chatInputResult struct {
	send   []string
	quit   bool
	editor bool // open $EDITOR on the current text
	clear  bool // clear the screen
}

func newChatInput(history []string) *chatInput {
	return &chatInput{history: history, histIdx: len(history), searchAt: -1}
}

// handle applies bytes read from the terminal.
func (in *chatInput) handle(b []byte) chatInputResult {
	var res chatInputResult
	b = append(in.pending, b...)
	in.pending = nil
	for len(b) > 0 {
		if in.pasting {
			b = in.paste(b)
			continue
		}
		k, n := nextKey(b)
		if n == 0 {
			in.pending = append([]byte(nil), b...)
			return res
		}
		b = b[n:]
		in.key(k, &res)
		if res.quit || res.editor {
			in.pending = append([]byte(nil), b...)
			return res
		}
	}
	return res
}

// flushEsc is called when no more bytes followed a lone ESC: it was the Esc key.
func (in *chatInput) flushEsc() chatInputResult {
	var res chatInputResult
	if in.pasting {
		return res // an ESC inside a paste is the start of its end marker, not the Esc key
	}
	if len(in.pending) > 0 && in.pending[0] == 0x1b {
		if string(in.pending) != "\x1b" {
			// An unfinished sequence such as Alt-[ followed by nothing: drop it rather than
			// let it swallow the next key.
			in.pending = nil
			return res
		}
		in.pending = nil
		in.key(key{kind: keyEsc}, &res)
	}
	return res
}

// escPending reports whether the room should start the short Esc wait.
func (in *chatInput) escPending() bool {
	return !in.pasting && len(in.pending) > 0 && in.pending[0] == 0x1b
}

// cleanText keeps what the input can draw: tabs become four spaces, line breaks stay, and other
// control characters are dropped. A tab or carriage return has no width as far as the layout knows,
// so either one made the drawn rows disagree with the screen.
func cleanText(s string) []rune {
	var out []rune
	for _, r := range strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n") {
		switch {
		case r == '\t':
			out = append(out, ' ', ' ', ' ', ' ')
		case r == '\n' || (r >= 32 && r != 0x7f && !(r >= 0x80 && r < 0xa0)):
			out = append(out, r)
		}
	}
	return out
}

// paste consumes bracketed-paste content up to the end marker and inserts it as one edit.
func (in *chatInput) paste(b []byte) []byte {
	end := strings.Index(string(b), pasteEnd)
	chunk, rest := b, []byte(nil)
	if end >= 0 {
		chunk, rest = b[:end], b[end+len(pasteEnd):]
		in.pasting = false
	} else if k := partialSuffix(b, pasteEnd); k > 0 {
		// The end marker may be split across two reads; hold its start for the next one, or paste
		// mode never ends and Enter and ctrl-c stop working.
		in.pending = append([]byte(nil), b[len(b)-k:]...)
		chunk = b[:len(b)-k]
	}
	in.ed.Insert(cleanText(string(chunk))...)
	return rest
}

func (in *chatInput) key(k key, res *chatInputResult) {
	if in.searching {
		in.searchKey(k, res)
		return
	}
	if k.kind != keyTab {
		in.hint = ""
	}
	e := &in.ed
	switch k.kind {
	case keyRune:
		e.Insert(k.r)
	case keyEnter:
		if e.cur > 0 && e.text[e.cur-1] == '\\' { // "\" then Enter: a line break, as in Claude Code
			e.Backspace()
			e.Insert('\n')
			return
		}
		if text := strings.TrimSpace(e.String()); text != "" {
			res.send = append(res.send, text)
			in.remember(text)
		}
		e.Reset()
		in.histIdx, in.histSave = len(in.history), ""
	case keyNewline:
		e.Insert('\n')
	case keyBackspace:
		e.Backspace()
	case keyDelete:
		e.Delete()
	case keyQuitIfEmpty:
		if len(e.text) == 0 {
			res.quit = true
		} else {
			e.Delete()
		}
	case keyLeft:
		e.Left()
	case keyRight:
		e.Right()
	case keyHome:
		e.Home()
	case keyEnd:
		e.End()
	case keyWordLeft:
		e.WordLeft()
	case keyWordRight:
		e.WordRight()
	case keyDeleteWordBack:
		e.DeleteWordBack()
	case keyDeleteWordFwd:
		e.DeleteWordFwd()
	case keyKillToStart:
		e.KillToStart()
	case keyKillToEnd:
		e.KillToEnd()
	case keyUndo:
		e.Undo()
	case keyUp:
		if !e.Up() {
			in.historyBack()
		}
	case keyDown:
		if !e.Down() {
			in.historyForward()
		}
	case keyTab:
		in.tab()
	case keyHistorySearch:
		in.searching, in.query, in.searchAt = true, "", -1
	case keyEsc:
		if time.Since(in.lastEsc) < chatDoubleEsc && len(e.text) > 0 {
			e.Set("") // Esc twice clears the input; ctrl-z brings it back
			in.lastEsc = time.Time{}
			return
		}
		in.lastEsc = time.Now()
		if in.histIdx < len(in.history) { // Esc while browsing history returns to the draft
			in.histIdx = len(in.history)
			e.Set(in.histSave)
		}
	case keyEditor:
		res.editor = true
	case keyClearScreen:
		res.clear = true
	case keyQuit:
		res.quit = true
	case keyPasteStart:
		in.pasting = true
	}
}

// remember adds a sent message to history, skipping an immediate repeat.
func (in *chatInput) remember(text string) {
	if n := len(in.history); n == 0 || in.history[n-1] != text {
		in.history = append(in.history, text)
	}
	in.histIdx = len(in.history)
}

func (in *chatInput) historyBack() {
	if in.histIdx == 0 {
		return
	}
	if in.histIdx == len(in.history) {
		in.histSave = in.ed.String()
	}
	in.histIdx--
	in.ed.Set(in.history[in.histIdx])
}

func (in *chatInput) historyForward() {
	if in.histIdx >= len(in.history) {
		return
	}
	in.histIdx++
	if in.histIdx == len(in.history) {
		in.ed.Set(in.histSave)
		return
	}
	in.ed.Set(in.history[in.histIdx])
}

// searchKey handles keys during ctrl-r: typing narrows, ctrl-r again finds an older match, Enter or
// an arrow takes the match into the input, Esc or ctrl-c leaves the input as it was.
func (in *chatInput) searchKey(k key, res *chatInputResult) {
	switch k.kind {
	case keyRune:
		in.query += string(k.r)
		in.searchFrom(len(in.history) - 1)
	case keyBackspace:
		if q := []rune(in.query); len(q) > 0 {
			in.query = string(q[:len(q)-1])
		}
		in.searchFrom(len(in.history) - 1)
	case keyHistorySearch:
		in.searchFrom(in.searchAt - 1)
	case keyEsc, keyQuit:
		in.searching = false
	default:
		in.searching = false
		if in.searchAt >= 0 {
			in.ed.Set(in.history[in.searchAt])
		}
		if k.kind != keyEnter {
			in.key(k, res)
		}
	}
}

func (in *chatInput) searchFrom(i int) {
	in.searchAt = -1
	if in.query == "" {
		return
	}
	q := strings.ToLower(in.query)
	for ; i >= 0; i-- {
		if strings.Contains(strings.ToLower(in.history[i]), q) {
			in.searchAt = i
			return
		}
	}
}

// tab completes the token before the cursor. One match is completed; several are completed to
// their common prefix and listed on the hint line.
func (in *chatInput) tab() {
	if in.complete == nil {
		return
	}
	token, from := in.ed.tokenBeforeCursor()
	cands := in.complete(token, string(in.ed.text[:from]))
	switch {
	case len(cands) == 1:
		in.ed.ReplaceToken(from, cands[0]+" ")
		in.hint = ""
	case len(cands) > 1:
		if p := commonPrefix(cands); len([]rune(p)) > len([]rune(token)) {
			in.ed.ReplaceToken(from, p)
		}
		in.hint = strings.Join(cands, "  ")
	}
}

func commonPrefix(ss []string) string {
	p := []rune(ss[0])
	for _, s := range ss[1:] {
		r := []rune(s)
		i := 0
		for i < len(p) && i < len(r) && p[i] == r[i] {
			i++
		}
		p = p[:i]
	}
	return string(p)
}

// --- drawing -------------------------------------------------------------------------------------

// clear returns the codes that move to the top of the input area and erase it.
func (in *chatInput) clear() string {
	s := ""
	if in.curRow > 0 {
		s = fmt.Sprintf("\x1b[%dA", in.curRow)
	}
	in.rows, in.curRow = 0, 0
	return s + "\r\x1b[J"
}

// layout cuts the buffer into screen rows for a window cols wide, and finds the cursor's row and
// column. Rows are cut here, never by the terminal, so the area always knows its own height.
func (in *chatInput) layout(cols int) (rows []string, curRow, curCol int) {
	prompt := chatPrompt
	text, cursor := in.ed.text, in.ed.cur
	if in.searching {
		prompt, text = "search › ", []rune(in.query)
		cursor = len(text)
	}
	pw := runewidth.StringWidth(prompt)
	pad := strings.Repeat(" ", pw)
	curRow = -1
	pos := 0 // buffer index of the current rune
	for li, line := range strings.Split(string(text), "\n") {
		lr := []rune(line)
		row, w := pad, pw
		if li == 0 {
			row = prompt
		}
		for i := 0; i <= len(lr); i++ {
			if pos+i == cursor && curRow < 0 {
				if w >= cols { // the cursor would sit past the edge: it starts the next row
					rows = append(rows, row)
					row, w = pad, pw
				}
				curRow, curCol = len(rows), w
			}
			if i == len(lr) {
				break
			}
			rw := runewidth.RuneWidth(lr[i])
			if w+rw > cols && w > pw { // start a new row, unless this one holds only the pad
				rows = append(rows, row)
				row, w = pad, pw
			}
			row += string(lr[i])
			w += rw
		}
		rows = append(rows, row)
		pos += len(lr) + 1
	}
	if curRow < 0 {
		curRow, curCol = len(rows)-1, runewidth.StringWidth(rows[len(rows)-1])
	}
	return rows, curRow, curCol
}

// draw returns the codes that redraw the whole input area for a window cols wide and leave the
// terminal cursor where the edit cursor is.
func (in *chatInput) draw(cols int) string {
	if cols < 20 {
		cols = 20 // narrower than the prompt plus a few characters cannot be laid out
	}
	var b strings.Builder
	b.WriteString(in.clear())
	var lines []string
	// A status or hint line must be exactly one row: a line break inside one made the area one row
	// taller than it counted, so every redraw left a row behind and the screen filled with copies.
	dim := func(s string) string {
		return "\x1b[2m" + runewidth.Truncate(oneRow(s), cols-1, "…") + "\x1b[0m"
	}
	for _, st := range in.status {
		lines = append(lines, dim(st))
	}
	if in.hint != "" {
		lines = append(lines, dim(in.hint))
	}
	if in.searching {
		match := "no match"
		if in.searchAt >= 0 {
			match = strings.ReplaceAll(in.history[in.searchAt], "\n", " ⏎ ")
		}
		lines = append(lines, dim("history: "+match))
	}
	top := len(lines)
	rows, curRow, curCol := in.layout(cols)
	// Show at most chatInputMaxRows rows of a long message, keeping the cursor in view.
	first := 0
	if len(rows) > chatInputMaxRows {
		first = min(max(0, curRow-chatInputMaxRows/2), len(rows)-chatInputMaxRows)
		rows = rows[first : first+chatInputMaxRows]
		curRow -= first
	}
	for i, r := range rows {
		if i == 0 && first == 0 {
			p := chatPrompt
			if in.searching {
				p = "search › "
			}
			r = tmuxTheme().ChatPrompt() + p + "\x1b[0m" + strings.TrimPrefix(r, p)
		}
		lines = append(lines, r)
	}
	b.WriteString(strings.Join(lines, "\r\n"))
	in.rows, in.curRow = len(lines), top+curRow
	if up := in.rows - 1 - in.curRow; up > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", up)
	}
	b.WriteString("\r")
	if curCol > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", curCol)
	}
	return b.String()
}

// oneRow flattens text for a single screen row: control characters, line breaks included, become
// spaces, and runs of spaces collapse.
func oneRow(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)), " ")
}

// partialSuffix is the length of the longest proper prefix of marker that b ends with.
func partialSuffix(b []byte, marker string) int {
	for k := min(len(b), len(marker)-1); k > 0; k-- {
		if string(b[len(b)-k:]) == marker[:k] {
			return k
		}
	}
	return 0
}
