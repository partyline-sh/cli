package main

import tea "charm.land/bubbletea/v2"

// chatKeysFor turns one Bubble Tea key event into the room's keys (chat_keys.go), so the editor's
// behaviour is exactly what it was: the same keys, the same actions. Every binding in
// docs/plans/ui-key-inventory.md "Chat" is here, and TestChatPopup sends each one.
func chatKeysFor(k tea.KeyPressMsg) []key {
	if kind, ok := chatKeyNames[k.String()]; ok {
		return []key{{kind: kind}}
	}
	// Plain text: each rune, as the decoder gave them. Keys held with ctrl or alt that are not
	// bindings do nothing, as before.
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		var out []key
		for _, r := range k.Text {
			out = append(out, key{kind: keyRune, r: r})
		}
		return out
	}
	return nil
}

// chatKeyNames maps Bubble Tea's key names to the room's keys. Terminals report some keys several
// ways (Option-← as alt+left or alt+b); each form is listed, as chat_keys.go decodes each.
var chatKeyNames = map[string]keyKind{
	"enter":          keyEnter,
	"shift+enter":    keyNewline,
	"alt+enter":      keyNewline,
	"ctrl+enter":     keyNewline,
	"ctrl+j":         keyEnter, // LF, which the decoder read as Enter
	"backspace":      keyBackspace,
	"ctrl+h":         keyBackspace,
	"delete":         keyDelete,
	"ctrl+d":         keyQuitIfEmpty, // delete forward, or leave on an empty line
	"left":           keyLeft,
	"ctrl+b":         keyLeft,
	"right":          keyRight,
	"ctrl+f":         keyRight,
	"home":           keyHome,
	"ctrl+a":         keyHome,
	"end":            keyEnd,
	"ctrl+e":         keyEnd,
	"alt+left":       keyWordLeft,
	"alt+b":          keyWordLeft,
	"ctrl+left":      keyWordLeft,
	"alt+right":      keyWordRight,
	"alt+f":          keyWordRight,
	"ctrl+right":     keyWordRight,
	"up":             keyUp,
	"down":           keyDown,
	"ctrl+w":         keyDeleteWordBack,
	"alt+backspace":  keyDeleteWordBack,
	"ctrl+backspace": keyDeleteWordBack,
	"alt+d":          keyDeleteWordFwd,
	"ctrl+u":         keyKillToStart,
	"ctrl+k":         keyKillToEnd,
	"ctrl+z":         keyUndo,
	"ctrl+_":         keyUndo,
	"tab":            keyTab,
	"ctrl+r":         keyHistorySearch,
	"ctrl+g":         keyEditor,
	"ctrl+l":         keyClearScreen,
	"ctrl+c":         keyQuit,
	"esc":            keyEsc,
	// The decoder read these regardless of a held modifier (chat_keys.go csiKey), and a modified
	// Backspace deleted a word.
	"shift+backspace": keyDeleteWordBack,
	"shift+left":      keyLeft,
	"shift+right":     keyRight,
	"shift+up":        keyUp,
	"shift+down":      keyDown,
	"shift+home":      keyHome,
	"shift+end":       keyEnd,
	"shift+delete":    keyDelete,
}
