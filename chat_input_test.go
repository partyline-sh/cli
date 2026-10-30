package main

import (
	"strings"
	"testing"
)

// typeKeys feeds bytes and returns the input's text and cursor.
func typeKeys(in *chatInput, s string) chatInputResult { return in.handle([]byte(s)) }

func TestChatEditingAtTheCursor(t *testing.T) {
	in := newChatInput(nil)
	typeKeys(in, "hello world")
	typeKeys(in, "\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D") // ← ×5: before "world"
	typeKeys(in, "big ")
	if got := in.ed.String(); got != "hello big world" {
		t.Fatalf("typing should insert at the cursor: %q", got)
	}
	typeKeys(in, "\x01") // ctrl-a
	typeKeys(in, "\x1b[3~")
	if got := in.ed.String(); got != "ello big world" {
		t.Errorf("Delete should remove the character under the cursor: %q", got)
	}
	typeKeys(in, "\x05\x1b\x7f") // ctrl-e, then Option-Backspace
	if got := in.ed.String(); got != "ello big " {
		t.Errorf("Option-Backspace should delete the word before the cursor: %q", got)
	}
	typeKeys(in, "\x1b[1;3D") // Option-← (CSI form)
	if in.ed.cur != 5 {
		t.Errorf("Option-← should move to the start of the previous word, cursor at %d", in.ed.cur)
	}
	typeKeys(in, "\x0b") // ctrl-k
	if got := in.ed.String(); got != "ello " {
		t.Errorf("ctrl-k should delete to the end of the line: %q", got)
	}
	typeKeys(in, "\x15") // ctrl-u, which Cmd-Backspace sends
	if got := in.ed.String(); got != "" {
		t.Errorf("ctrl-u should delete to the start of the line: %q", got)
	}
	typeKeys(in, "\x1a") // ctrl-z
	if got := in.ed.String(); got != "ello " {
		t.Errorf("undo should bring back what ctrl-u deleted: %q", got)
	}
}

func TestChatUndoRemovesAWordAtATime(t *testing.T) {
	in := newChatInput(nil)
	typeKeys(in, "one two")
	typeKeys(in, "\x1f") // ctrl-_
	if got := in.ed.String(); got != "one " {
		t.Errorf("undo should remove the last typed word, not one character: %q", got)
	}
}

func TestChatNewlines(t *testing.T) {
	for name, seq := range map[string]string{
		"Alt-Enter":                    "\x1b\r",
		"Shift-Enter, modifyOtherKeys": "\x1b[27;2;13~",
		"Shift-Enter, CSI u":           "\x1b[13;2u",
		"backslash then Enter":         "\\\r",
	} {
		in := newChatInput(nil)
		res := typeKeys(in, "one"+seq+"two\r")
		if len(res.send) != 1 || res.send[0] != "one\ntwo" {
			t.Errorf("%s should add a line break, not send: %q", name, res.send)
		}
	}
}

func TestChatUpDownMoveBetweenLinesThenHistory(t *testing.T) {
	in := newChatInput([]string{"first", "second"})
	typeKeys(in, "draft line1\x1b\rline2")
	typeKeys(in, "\x1b[A") // ↑ inside a two-line draft: to line 1
	if in.ed.String() != "draft line1\nline2" || in.ed.cur > len("draft line1") {
		t.Fatalf("↑ in a multi-line draft should move up a line, not recall history (cursor %d)", in.ed.cur)
	}
	typeKeys(in, "\x1b[A") // ↑ on the first line: history
	if in.ed.String() != "second" {
		t.Fatalf("↑ on the first line should recall the last message: %q", in.ed.String())
	}
	typeKeys(in, "\x1b[A")
	if in.ed.String() != "first" {
		t.Errorf("↑ again should go further back: %q", in.ed.String())
	}
	typeKeys(in, "\x1b[B\x1b[B")
	if in.ed.String() != "draft line1\nline2" {
		t.Errorf("↓ past the newest should restore the draft: %q", in.ed.String())
	}
}

func TestChatHistorySearch(t *testing.T) {
	in := newChatInput([]string{"deploy the api", "check the logs", "deploy the frontend"})
	typeKeys(in, "\x12dep") // ctrl-r, "dep"
	if !in.searching || in.history[in.searchAt] != "deploy the frontend" {
		t.Fatalf("search should find the newest match")
	}
	typeKeys(in, "\x12") // older match
	if in.history[in.searchAt] != "deploy the api" {
		t.Errorf("ctrl-r again should find an older match: %q", in.history[in.searchAt])
	}
	if res := typeKeys(in, "\r"); len(res.send) != 0 || in.ed.String() != "deploy the api" || in.searching {
		t.Errorf("Enter should put the match in the input without sending it: %q sent=%v", in.ed.String(), res.send)
	}
}

func TestChatTabCompletion(t *testing.T) {
	in := newChatInput(nil)
	in.complete = func(token, before string) []string {
		var out []string
		for _, c := range []string{"@10", "@11", "@codex"} {
			if strings.HasPrefix(c, token) {
				out = append(out, c)
			}
		}
		return out
	}
	typeKeys(in, "hi @c\t")
	if got := in.ed.String(); got != "hi @codex " {
		t.Errorf("one match should complete with a space: %q", got)
	}
	in.ed.Reset()
	typeKeys(in, "@1\t")
	if in.ed.String() != "@1" || !strings.Contains(in.hint, "@10") || !strings.Contains(in.hint, "@11") {
		t.Errorf("several matches should be listed on the hint line: %q hint %q", in.ed.String(), in.hint)
	}
}

func TestChatEscapeKeys(t *testing.T) {
	in := newChatInput(nil)
	typeKeys(in, "draft")
	typeKeys(in, "\x1b") // a lone ESC waits
	if string(in.pending) != "\x1b" {
		t.Fatal("a lone ESC should wait for the next bytes")
	}
	in.flushEsc()
	typeKeys(in, "\x1b")
	in.flushEsc()
	if in.ed.String() != "" {
		t.Errorf("Esc twice should clear the input: %q", in.ed.String())
	}
	typeKeys(in, "\x1a")
	if in.ed.String() != "draft" {
		t.Errorf("undo should bring the cleared draft back: %q", in.ed.String())
	}
	// An arrow split across reads must not leak into the text.
	typeKeys(in, "\x1b[")
	typeKeys(in, "D!")
	if in.ed.String() != "draf!t" {
		t.Errorf("a split arrow sequence should move the cursor: %q", in.ed.String())
	}
}

// The input area knows its height and where the cursor is, so a redraw clears exactly what it drew.
func TestChatInputLayout(t *testing.T) {
	in := newChatInput(nil)
	in.ed.Set(strings.Repeat("x", 150))
	rows, curRow, curCol := in.layout(80)
	// Continuation rows are indented under the prompt: 74 characters per row, so 150 take three.
	if len(rows) != 3 || curRow != 2 || curCol != 6+2 {
		t.Fatalf("150 characters in 80 columns with a 6-column prompt: rows=%d cursor=%d,%d", len(rows), curRow, curCol)
	}
	in.ed.Home()
	if _, curRow, curCol = in.layout(80); curRow != 0 || curCol != 6 {
		t.Errorf("Home should put the cursor after the prompt: %d,%d", curRow, curCol)
	}
	in.status = []string{"· #11 codex · waiting"}
	out := in.draw(80)
	if in.rows != 4 || in.curRow != 1 {
		t.Errorf("status plus three input rows with the cursor on the first: rows=%d curRow=%d", in.rows, in.curRow)
	}
	if !strings.HasPrefix(in.draw(80), "\x1b[1A\r\x1b[J") {
		t.Error("a redraw should move up to the area's top from the cursor's row before clearing")
	}
	_ = out
	in.ed.Set(strings.Repeat("line\n", 40))
	in.draw(80)
	if in.rows > chatInputMaxRows+1 {
		t.Errorf("a long draft should be capped at %d rows, drew %d", chatInputMaxRows, in.rows)
	}
	in.ed.Set("日本語")
	if _, _, c := in.layout(80); c != 6+6 {
		t.Errorf("wide characters take two columns each: cursor column %d", c)
	}
}

func TestChatKeysDecode(t *testing.T) {
	cases := map[string]keyKind{
		"\x1b[H": keyHome, "\x1b[1~": keyHome, "\x1bOH": keyHome, "\x1b[7~": keyHome,
		"\x1b[F": keyEnd, "\x1b[4~": keyEnd, "\x1b[3~": keyDelete,
		"\x1bb": keyWordLeft, "\x1bf": keyWordRight, "\x1b[1;5C": keyWordRight,
		"\x7f": keyBackspace, "\x08": keyBackspace, "\x1b[127;3u": keyDeleteWordBack,
		"\x12": keyHistorySearch, "\x07": keyEditor, "\x0c": keyClearScreen, "\x03": keyQuit,
	}
	for seq, want := range cases {
		if k, n := nextKey([]byte(seq)); k.kind != want || n != len(seq) {
			t.Errorf("%q: got kind %d (%d bytes), want %d", seq, k.kind, n, want)
		}
	}
}

// Review fixes on the editor: an ESC that starts the paste-end marker is not the Esc key, tabs and
// control characters cannot reach the layout, an unfinished Alt-[ does not eat the next key, and
// history browsing is reset by Enter and Esc.
func TestChatEditorReviewFixes(t *testing.T) {
	in := newChatInput(nil)
	typeKeys(in, pasteStart+"hello\x1b")
	if in.escPending() {
		t.Fatal("an ESC inside a paste must not start the Esc wait")
	}
	in.flushEsc() // even if the wait fired, nothing may change mid-paste
	typeKeys(in, "[201~")
	if in.pasting || in.ed.String() != "hello" {
		t.Fatalf("the paste should end cleanly: pasting=%v text=%q", in.pasting, in.ed.String())
	}

	in = newChatInput(nil)
	typeKeys(in, pasteStart+"a\tb\rc\x1b[31mred"+pasteEnd)
	if got := in.ed.String(); got != "a    b\nc[31mred" {
		t.Errorf("tabs should become spaces and control characters be dropped: %q", got)
	}
	in.ed.Set("x\ty\x07z")
	if got := in.ed.String(); got != "x    yz" {
		t.Errorf("Set should clean text the same way: %q", got)
	}

	in = newChatInput(nil)
	typeKeys(in, "\x1b[")
	if !in.escPending() {
		t.Fatal("an unfinished sequence should start the Esc wait")
	}
	in.flushEsc()
	typeKeys(in, "a")
	if in.ed.String() != "a" {
		t.Errorf("after an unfinished Alt-[ the next key should be typed: %q", in.ed.String())
	}

	in = newChatInput([]string{"old"})
	typeKeys(in, "draft\x1b[A") // ↑: browse to "old"
	typeKeys(in, "\x1b")
	in.flushEsc()
	if in.ed.String() != "draft" {
		t.Errorf("Esc while browsing history should return to the draft: %q", in.ed.String())
	}
}

// A status line with a line break inside (a command like curl -w '%{http_code}\n') must still be
// drawn as one row, or the area counts fewer rows than it printed and each redraw leaves one behind.
func TestChatStatusLineIsOneRow(t *testing.T) {
	in := newChatInput(nil)
	in.status = []string{"⠋ #11 codex · digging in… 1m21s · running curl -w 'code: %{http_code}\n' -d x\r\nhttp://127.0.0.1"}
	out := in.draw(80)
	printed := strings.Count(out, "\r\n") + 1
	if printed != in.rows || strings.Contains(stripColours(out), "\n\n") {
		t.Fatalf("drew %d rows but counted %d", printed, in.rows)
	}
	if strings.Count(out, "\n") != strings.Count(out, "\r\n") {
		t.Errorf("a line break was printed outside the row separators: %q", out)
	}
	if c := clipDoing("running " + strings.Repeat("x", 200)); len([]rune(c)) > 60 {
		t.Errorf("tool summaries should be capped at 60 columns: %d", len([]rune(c)))
	}
	if !chatPlumbing(`{"command":"ptln chat listen","description":"Listen for chat messages"}`) {
		t.Error("the room's own commands should not count as what a session is doing")
	}
}
