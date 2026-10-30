package main

// The edit buffer behind the room's input: text, a cursor, and undo. It knows nothing about the
// terminal; chat_input.go turns keys into calls here and draws the result.

import "unicode"

type editBuf struct {
	text []rune
	cur  int // cursor position, 0..len(text)
	undo []editSnap
	// lastKind coalesces runs of typing into one undo step, so undo removes a word at a time
	// rather than a character.
	lastKind string
}

type editSnap struct {
	text []rune
	cur  int
}

const editUndoMax = 200

// snapshot records the state before an edit of the given kind.
func (e *editBuf) snapshot(kind string) {
	if kind == "type" && e.lastKind == "type" {
		return
	}
	e.lastKind = kind
	e.undo = append(e.undo, editSnap{append([]rune(nil), e.text...), e.cur})
	if len(e.undo) > editUndoMax {
		e.undo = e.undo[1:]
	}
}

func (e *editBuf) Undo() {
	if len(e.undo) == 0 {
		return
	}
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.text, e.cur, e.lastKind = s.text, s.cur, ""
}

func (e *editBuf) String() string { return string(e.text) }

// Set replaces the whole text (history, completion, the external editor) as one undo step.
func (e *editBuf) Set(s string) {
	e.snapshot("set")
	e.text = cleanText(s)
	e.cur = len(e.text)
}

// Reset empties the buffer and its undo history (after sending).
func (e *editBuf) Reset() { *e = editBuf{} }

func (e *editBuf) Insert(rs ...rune) {
	if len(rs) == 0 {
		return
	}
	kind := "type"
	if len(rs) > 1 || rs[0] == '\n' || unicode.IsSpace(rs[0]) {
		kind = "insert" // a space or line break ends a typing run
	}
	e.snapshot(kind)
	e.text = append(e.text[:e.cur], append(append([]rune(nil), rs...), e.text[e.cur:]...)...)
	e.cur += len(rs)
}

func (e *editBuf) Backspace() {
	if e.cur == 0 {
		return
	}
	e.snapshot("delete")
	e.text = append(e.text[:e.cur-1], e.text[e.cur:]...)
	e.cur--
}

func (e *editBuf) Delete() {
	if e.cur >= len(e.text) {
		return
	}
	e.snapshot("delete")
	e.text = append(e.text[:e.cur], e.text[e.cur+1:]...)
}

func (e *editBuf) Left() {
	if e.cur > 0 {
		e.cur--
	}
	e.lastKind = ""
}

func (e *editBuf) Right() {
	if e.cur < len(e.text) {
		e.cur++
	}
	e.lastKind = ""
}

// lineStart and lineEnd bound the line the cursor is on (lines are separated by '\n').
func (e *editBuf) lineStart() int {
	i := e.cur
	for i > 0 && e.text[i-1] != '\n' {
		i--
	}
	return i
}

func (e *editBuf) lineEnd() int {
	i := e.cur
	for i < len(e.text) && e.text[i] != '\n' {
		i++
	}
	return i
}

func (e *editBuf) Home() { e.cur, e.lastKind = e.lineStart(), "" }
func (e *editBuf) End()  { e.cur, e.lastKind = e.lineEnd(), "" }

// wordLeft is where the cursor lands moving back one word: over spaces, then over the word.
func (e *editBuf) wordLeft() int {
	i := e.cur
	for i > 0 && !isWordRune(e.text[i-1]) {
		i--
	}
	for i > 0 && isWordRune(e.text[i-1]) {
		i--
	}
	return i
}

func (e *editBuf) wordRight() int {
	i := e.cur
	for i < len(e.text) && !isWordRune(e.text[i]) {
		i++
	}
	for i < len(e.text) && isWordRune(e.text[i]) {
		i++
	}
	return i
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

func (e *editBuf) WordLeft()  { e.cur, e.lastKind = e.wordLeft(), "" }
func (e *editBuf) WordRight() { e.cur, e.lastKind = e.wordRight(), "" }

// cut removes text[from:to] as one undo step and leaves the cursor at from.
func (e *editBuf) cut(from, to int) {
	if from >= to {
		return
	}
	e.snapshot("cut")
	e.text = append(e.text[:from], e.text[to:]...)
	e.cur = from
}

func (e *editBuf) DeleteWordBack() { e.cut(e.wordLeft(), e.cur) }
func (e *editBuf) DeleteWordFwd()  { e.cut(e.cur, e.wordRight()) }

// KillToStart deletes back to the start of the line; at the start of a line it joins it to the
// previous one, so repeated presses keep deleting.
func (e *editBuf) KillToStart() {
	from := e.lineStart()
	if from == e.cur && e.cur > 0 {
		from = e.cur - 1
	}
	e.cut(from, e.cur)
}

// KillToEnd deletes to the end of the line; at the end of a line it removes the line break.
func (e *editBuf) KillToEnd() {
	to := e.lineEnd()
	if to == e.cur && e.cur < len(e.text) {
		to = e.cur + 1
	}
	e.cut(e.cur, to)
}

// Up and Down move between lines of a multi-line message, keeping the column where possible. They
// report false on the first or last line, which is where history takes over.
func (e *editBuf) Up() bool {
	start := e.lineStart()
	if start == 0 {
		return false
	}
	col := e.cur - start
	prevEnd := start - 1
	prevStart := prevEnd
	for prevStart > 0 && e.text[prevStart-1] != '\n' {
		prevStart--
	}
	e.cur, e.lastKind = min(prevStart+col, prevEnd), ""
	return true
}

func (e *editBuf) Down() bool {
	end := e.lineEnd()
	if end >= len(e.text) {
		return false
	}
	col := e.cur - e.lineStart()
	nextStart := end + 1
	nextEnd := nextStart
	for nextEnd < len(e.text) && e.text[nextEnd] != '\n' {
		nextEnd++
	}
	e.cur, e.lastKind = min(nextStart+col, nextEnd), ""
	return true
}

// tokenBeforeCursor is the run of non-space characters ending at the cursor, for completion.
func (e *editBuf) tokenBeforeCursor() (string, int) {
	i := e.cur
	for i > 0 && !unicode.IsSpace(e.text[i-1]) {
		i--
	}
	return string(e.text[i:e.cur]), i
}

// ReplaceToken swaps text[from:cur] for s, as one undo step.
func (e *editBuf) ReplaceToken(from int, s string) {
	e.snapshot("complete")
	rs := []rune(s)
	e.text = append(e.text[:from], append(rs, e.text[e.cur:]...)...)
	e.cur = from + len(rs)
}
