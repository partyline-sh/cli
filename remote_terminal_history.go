package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/remote"
)

// Read scrollback without entering tmux copy mode or sending keystrokes to the
// shared terminal. Bound the result independently of the pane's history-limit.
func remoteTerminalHistory(m remote.Message) (string, string, any) {
	if m.Data != nil || !remoteSessionPane.MatchString(m.Pane) {
		return "refused", "Invalid terminal history request", nil
	}
	inventory, err := remoteSnapshot("sessions", "focus")
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote terminal inventory read failed:", err)
		return "refused", "Could not read terminal inventory; refresh and try again", nil
	}
	found := false
	for _, row := range inventory.([]map[string]string) {
		if row["pane"] == m.Pane {
			found = true
			break
		}
	}
	if !found {
		return "refused", "Terminal is no longer available", nil
	}
	meta, err := tmuxCmd("display-message", "-p", "-t", m.Pane, "#{pane_width}|#{history_size}|#{alternate_on}").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote terminal state read failed:", err)
		return "refused", "Could not read terminal state; refresh and try again", nil
	}
	fields := strings.Split(strings.TrimSpace(string(meta)), "|")
	if len(fields) != 3 {
		return "refused", "Could not read terminal dimensions", nil
	}
	columns, _ := strconv.Atoi(fields[0])
	history, _ := strconv.Atoi(fields[1])
	out, err := tmuxCmd("capture-pane", "-e", "-p", "-S", "-2000", "-t", m.Pane).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote terminal history capture failed:", err)
		return "refused", "Could not load terminal history; refresh and try again", nil
	}
	text, limited := boundTerminalHistory(string(out), 448*1024)
	data := map[string]any{"ansi": text, "columns": columns, "limited": limited || history > 2000, "alternate": fields[2] == "1"}
	for remoteDetailSize(data) != nil {
		text, _ = boundTerminalHistory(text, len(text)/2)
		data["ansi"] = text
		data["limited"] = true
	}
	return "ok", "", data
}
func boundTerminalHistory(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	start := len(text) - limit
	if line := strings.IndexByte(text[start:], '\n'); line >= 0 {
		return text[start+line+1:], true
	}
	return "", true
}
