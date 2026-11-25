package main

import (
	xansi "github.com/charmbracelet/x/ansi"
	"strings"
)

// Wrap between complete buttons so every footer action remains clickable.
func relayKeyRows(keys []string, width int) string {
	var rows []string
	line := ""
	for _, key := range keys {
		candidate := key
		if line != "" {
			candidate = line + "   " + key
		}
		if line != "" && xansi.StringWidth(candidate) > width {
			rows = append(rows, line)
			line = key
		} else {
			line = candidate
		}
	}
	if line != "" {
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}

func (m relayModel) footerKeys(st gateStyle) []string {
	keys := []string{st.key("d", "deploy")}
	if m.v.set {
		keys = []string{st.key("u", "update the relay"), st.key("p", "pair a phone")}
	}
	if m.v.hosting == "self-hosted" {
		keys = nil
		if !m.v.serverOnly {
			keys = append(keys, st.key("p", "pair a phone"))
		}
		if m.v.owner {
			keys = append(keys, st.key("a", "approve computer"))
		}
		if m.v.localHost {
			keys = append(keys, st.key("r", "restart service"))
		}
	} else {
		keys = append(keys, st.key("t", "save a token"))
	}
	if m.v.otherGateway {
		keys = append(keys, st.key("g", "private gateway"))
	}
	if m.v.saved {
		keys = append(keys, st.key("x", "forget it"))
	}
	keys = append(keys, st.key("s", "setup gateway"), st.key("esc", "close"))
	if m.edit {
		keys = []string{st.key("⏎", "save"), st.key("esc", "cancel")}
	}
	if m.confirm != "" {
		keys = []string{st.key("⏎", "confirm restart"), st.key("esc", "cancel")}
	}
	return keys
}
