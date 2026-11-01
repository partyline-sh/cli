package main

// Turning terminal bytes into keys for the room's input.
//
// Terminals report the same key several ways: Home is ESC[H, ESC[1~, ESC[7~ or ESCOH depending on
// the terminal; Option-← on a Mac is ESC b in some and ESC[1;3D in others; Shift-Enter only differs
// from Enter when the terminal is asked to report modified keys (modifyOtherKeys, which partyline's
// tmux supports, or the CSI u form some terminals use). Every form maps to one key here, so the
// editor only deals in keys.

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

type keyKind int

const (
	keyRune keyKind = iota
	keyEnter
	keyNewline // Shift-Enter, Alt-Enter
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyUp
	keyDown
	keyHome
	keyEnd
	keyWordLeft
	keyWordRight
	keyDeleteWordBack
	keyDeleteWordFwd
	keyKillToStart
	keyKillToEnd
	keyUndo
	keyTab
	keyEsc
	keyHistorySearch
	keyEditor
	keyClearScreen
	keyQuit
	keyQuitIfEmpty
	keyPasteStart
	keyIgnore
)

type key struct {
	kind keyKind
	r    rune
}

// Ask the terminal to report modified keys, so Shift-Enter arrives as its own sequence.
const modifyKeysOn, modifyKeysOff = "\x1b[>4;1m", "\x1b[>4;0m"

// nextKey decodes one key from the start of b. n is the number of bytes it used; n == 0 means b
// holds only the start of a sequence and more bytes are needed.
func nextKey(b []byte) (k key, n int) {
	if len(b) == 0 {
		return key{kind: keyIgnore}, 0
	}
	c := b[0]
	if c == 0x1b {
		return escKey(b)
	}
	switch c {
	case '\r', '\n':
		return key{kind: keyEnter}, 1
	case 127, 8:
		return key{kind: keyBackspace}, 1
	case '\t':
		return key{kind: keyTab}, 1
	case 1:
		return key{kind: keyHome}, 1
	case 5:
		return key{kind: keyEnd}, 1
	case 2:
		return key{kind: keyLeft}, 1
	case 6:
		return key{kind: keyRight}, 1
	case 4:
		return key{kind: keyQuitIfEmpty}, 1 // ctrl-d: delete forward, or leave on an empty line
	case 11:
		return key{kind: keyKillToEnd}, 1
	case 21:
		return key{kind: keyKillToStart}, 1 // ctrl-u, which Cmd-Backspace sends in most Mac terminals
	case 23:
		return key{kind: keyDeleteWordBack}, 1
	case 31, 26:
		return key{kind: keyUndo}, 1 // ctrl-_ and ctrl-z
	case 18:
		return key{kind: keyHistorySearch}, 1
	case 7:
		return key{kind: keyEditor}, 1
	case 12:
		return key{kind: keyClearScreen}, 1
	case 3:
		return key{kind: keyQuit}, 1
	}
	if c < 0x20 {
		return key{kind: keyIgnore}, 1
	}
	if !utf8.FullRune(b) {
		return key{kind: keyIgnore}, 0
	}
	r, size := utf8.DecodeRune(b)
	return key{kind: keyRune, r: r}, size
}

// escKey decodes a sequence starting with ESC. A lone ESC returns n == 0: the caller decides, after
// a short wait, whether it was the Esc key or the start of a sequence still arriving.
func escKey(b []byte) (key, int) {
	if len(b) < 2 {
		return key{}, 0
	}
	switch b[1] {
	case '[':
		return csiKey(b)
	case 'O':
		if len(b) < 3 {
			return key{}, 0
		}
		switch b[2] {
		case 'H':
			return key{kind: keyHome}, 3
		case 'F':
			return key{kind: keyEnd}, 3
		case 'A':
			return key{kind: keyUp}, 3
		case 'B':
			return key{kind: keyDown}, 3
		case 'C':
			return key{kind: keyRight}, 3
		case 'D':
			return key{kind: keyLeft}, 3
		}
		return key{kind: keyIgnore}, 3
	case '\r', '\n':
		return key{kind: keyNewline}, 2 // Alt-Enter
	case 127, 8:
		return key{kind: keyDeleteWordBack}, 2 // Option-Backspace
	case 'b', 'B':
		return key{kind: keyWordLeft}, 2 // Option-← in terminals that send Meta-b
	case 'f', 'F':
		return key{kind: keyWordRight}, 2
	case 'd', 'D':
		return key{kind: keyDeleteWordFwd}, 2
	case 0x1b:
		return key{kind: keyEsc}, 1 // ESC ESC: the first is a plain Esc
	}
	_, size := utf8.DecodeRune(b[1:])
	return key{kind: keyIgnore}, 1 + size // Alt plus another key
}

// csiKey decodes ESC [ parameters final.
func csiKey(b []byte) (key, int) {
	end := -1
	for i := 2; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			end = i
			break
		}
	}
	if end < 0 {
		return key{}, 0
	}
	n := end + 1
	seq := string(b[:n])
	if seq == pasteStart {
		return key{kind: keyPasteStart}, n
	}
	params := strings.Split(string(b[2:end]), ";")
	mod := 1
	if len(params) > 1 {
		mod, _ = strconv.Atoi(params[1])
	}
	alt, ctrl := mod == 3 || mod == 4 || mod == 7 || mod == 8, mod >= 5
	switch b[end] {
	case 'A':
		return key{kind: keyUp}, n
	case 'B':
		return key{kind: keyDown}, n
	case 'C':
		if alt || ctrl {
			return key{kind: keyWordRight}, n
		}
		return key{kind: keyRight}, n
	case 'D':
		if alt || ctrl {
			return key{kind: keyWordLeft}, n
		}
		return key{kind: keyLeft}, n
	case 'H':
		return key{kind: keyHome}, n
	case 'F':
		return key{kind: keyEnd}, n
	case 'u': // CSI u: codepoint;modifier
		cp, _ := strconv.Atoi(params[0])
		return modifiedKey(cp, mod), n
	case '~':
		switch params[0] {
		case "1", "7":
			return key{kind: keyHome}, n
		case "4", "8":
			return key{kind: keyEnd}, n
		case "3":
			return key{kind: keyDelete}, n
		case "27": // modifyOtherKeys: 27;modifier;codepoint
			if len(params) == 3 {
				cp, _ := strconv.Atoi(params[2])
				return modifiedKey(cp, mod), n
			}
		}
	}
	return key{kind: keyIgnore}, n
}

// modifiedKey maps a key reported with modifiers back to an editor key.
func modifiedKey(cp, mod int) key {
	switch cp {
	case 13:
		if mod > 1 {
			return key{kind: keyNewline} // Shift-Enter, Ctrl-Enter, Alt-Enter
		}
		return key{kind: keyEnter}
	case 127, 8:
		if mod > 1 {
			return key{kind: keyDeleteWordBack}
		}
		return key{kind: keyBackspace}
	case 9:
		return key{kind: keyTab}
	case 27:
		return key{kind: keyEsc}
	}
	if mod == 5 && cp >= 'a' && cp <= 'z' { // ctrl-letter reported as CSI u
		k, _ := nextKey([]byte{byte(cp - 'a' + 1)})
		return k
	}
	if cp >= 32 && mod <= 2 {
		return key{kind: keyRune, r: rune(cp)}
	}
	return key{kind: keyIgnore}
}
