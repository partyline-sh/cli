package main

import (
	"fmt"
	"strconv"
	"strings"
)

func (r *chatRoom) limitLabel() string {
	if r.cap() < 0 {
		return "∞"
	}
	return strconv.Itoa(r.cap())
}

func (r *chatRoom) limitMeter() string {
	label := fmt.Sprintf("− + agents %d/%s", r.AgentRun, r.limitLabel())
	if r.cap() < 0 {
		return label + " · limit OFF"
	}
	filled := min(6, r.AgentRun*6/r.cap())
	label += " " + strings.Repeat("▮", filled) + strings.Repeat("▯", 6-filled)
	if r.Stopped {
		return label + " · stopped: /go"
	}
	if r.paused() {
		return label + " · paused: say anything or press +"
	}
	return label + " since you spoke"
}

func chatLimit(args []string) string {
	if len(args) != 1 {
		return "usage: /limit <4–1000000|off>"
	}
	limit := -1
	if args[0] != "off" {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 4 || n > 1000000 {
			return "usage: /limit <4–1000000|off>"
		}
		limit = n
	}
	err := withChatLock(func(r *chatRoom) error { r.AgentCap = limit; return nil })
	if err != nil {
		return "limit: " + err.Error()
	}
	// Listeners and the watcher re-read the persisted room on their next poll, just as
	// after a person's message. Do not clear read marks, manual stops or wake safety checks.
	if limit < 0 {
		return "agent limit off; unattended replies are unlimited"
	}
	return fmt.Sprintf("agent limit set to %d", limit)
}

func (m *chatPopup) pickLimitAt(x, y int) bool {
	_, room, _, _, _, _ := m.layout(gateStyle{m.motion.theme()})
	x -= m.margin()
	if y != room+1 || (x != 0 && x != 2) {
		return false
	}
	// Re-read under the same lock as the update to avoid losing another panel's change.
	err := withChatLock(func(r *chatRoom) error {
		limit := r.cap()
		if limit < 0 {
			limit = chatAgentRunCap
		} else if x == 0 {
			limit = max(4, limit-4)
		} else {
			limit = min(1000000, limit+4)
		}
		r.AgentCap = limit
		m.room = *r
		return nil
	})
	if err != nil {
		m.in.hint = "limit: " + err.Error()
	}
	m.selection = chatSelection{}
	return true
}
