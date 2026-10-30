package main

// The room's commands, completion and history, shared by the chat popup (chat_popup.go). Waking idle
// members is the watcher's job (watcher_tick.go).

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func chatDraftPath() string { return filepath.Join(chatDir(), "draft.txt") }

// chatHistory is what you sent in this room, oldest first, for ↑ and ctrl-r.
func chatHistory(msgs []chatMessage) []string {
	var h []string
	for _, m := range msgs {
		if m.From == "you" && (len(h) == 0 || h[len(h)-1] != m.Text) {
			h = append(h, m.Text)
		}
	}
	return h
}

var chatCommands = []string{"/add", "/remove", "/who", "/stop", "/go", "/clear", "/help"}

// chatComplete returns completions for a token: room commands at the start of the line, tab
// numbers after /add, member numbers after /remove /stop /go, and @mentions anywhere.
func chatComplete(token, before string) []string {
	var out []string
	add := func(c string) {
		if strings.HasPrefix(c, token) && c != token {
			out = append(out, c)
		}
	}
	first := strings.Fields(before)
	switch {
	case strings.TrimSpace(before) == "" && strings.HasPrefix(token, "/"):
		for _, c := range chatCommands {
			add(c)
		}
	case len(first) > 0 && first[0] == "/add":
		room := loadChatRoom()
		for _, p := range chatPanes() {
			if room.member(p.Key) == nil {
				add(strings.TrimPrefix(strings.Fields(p.Name)[0], "#"))
			}
		}
	case len(first) > 0 && (first[0] == "/remove" || first[0] == "/stop" || first[0] == "/go"):
		for _, m := range loadChatRoom().Members {
			add(strings.TrimPrefix(strings.Fields(m.Name)[0], "#"))
		}
	case strings.HasPrefix(token, "@"):
		room := loadChatRoom()
		add("@all")
		seen := map[string]bool{}
		for _, m := range room.Members {
			for _, c := range []string{"@" + strings.TrimPrefix(strings.Fields(m.Name)[0], "#"), "@" + m.Engine} {
				if !seen[c] {
					seen[c] = true
					add(c)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, aerr := strconv.ParseFloat(strings.TrimLeft(out[i], "@/#"), 64)
		b, berr := strconv.ParseFloat(strings.TrimLeft(out[j], "@/#"), 64)
		if aerr == nil && berr == nil {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}

const chatRoomHelp = `commands: /add <tab>... · /remove <tab>... · /who · /stop [tab...] · /go [tab...] · /clear · /help
send: ⏎ · new line: Shift-⏎, Alt-⏎ or \ then ⏎ · address one session: @10 or @codex · Tab completes
edit: ←/→ · Option-←/→ or Alt-b/f by word · Home/End or ctrl-a/ctrl-e · ↑/↓ between lines, then history
delete: Backspace · Delete or ctrl-d · Option-Backspace or ctrl-w a word · ctrl-u to line start · ctrl-k to line end
history: wheel scroll · drag to copy · PgUp/PgDn (fn-↑/↓ on a Mac) scroll the whole room · Esc back to the newest
selection: hold Shift (Option in iTerm2) for the terminal’s own selection
also: ctrl-z or ctrl-_ undo · ctrl-r search what you sent · ctrl-g open $EDITOR · Esc Esc clear · ctrl-l clear screen · ctrl-t pin as a tab · ctrl-c leave`

func chatSubmit(line string, say func(string), clearScreen func()) {
	f := strings.Fields(line)
	switch f[0] {
	case "/add":
		say("\x1b[2m" + chatAdd(f[1:]) + "\x1b[0m")
		return
	case "/remove", "/rm":
		say("\x1b[2m" + chatRemove(f[1:]) + "\x1b[0m")
		return
	case "/who":
		say("\x1b[2m" + strings.TrimRight(chatWho(), "\n") + "\x1b[0m")
		return
	case "/stop":
		say("\x1b[2m" + chatStop(f[1:], true) + "\x1b[0m")
		return
	case "/go":
		say("\x1b[2m" + chatStop(f[1:], false) + "\x1b[0m")
		return
	case "/clear":
		clearScreen()
		return
	case "/help":
		say("\x1b[2m" + chatRoomHelp + "\x1b[0m")
		return
	}
	if err := chatSendPerson(line); err != nil {
		say("\x1b[2m" + err.Error() + "\x1b[0m")
	}
}

// Shared person posting path for the desktop room and paired phones.
func chatSendPerson(line string) error {
	return withChatLock(func(r *chatRoom) error {
		to := chatAddressees(r, line)
		if to == nil && chatHasMention(line) {
			return fmt.Errorf("no session in the room matches the @ in that message; it was not sent (use @all for everyone)")
		}
		_, err := postLocked(r, chatMessage{From: "you", Name: "you", To: to, Text: line})
		return err
	})
}
