package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/remote"
)

type liveScreen struct {
	PID        string `json:"-"`
	Typing     bool   `json:"typing"`
	Foreground string `json:"foreground"`
	ANSI       string `json:"ansi"`
	Columns    int    `json:"columns"`
	Rows       int    `json:"rows"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Cursor     bool   `json:"cursor"`
	Alternate  bool   `json:"alternate"`
	Copy       bool   `json:"copy"`
	Accepted   uint64 `json:"acceptedSeq"`
	Echo       uint64 `json:"echoSeq"`
}

const liveScreenFormat = "#{pane_pid}|#{pane_width}|#{pane_height}|#{cursor_x}|#{cursor_y}|#{cursor_flag}|#{alternate_on}|#{pane_in_mode}"

// One tmux command queue captures metadata on both sides of the screen. A cursor
// change during capture is deferred instead of pairing text with a wrong cursor.
func captureLiveScreen(pane string) (liveScreen, error) {
	var screen liveScreen
	out, err := tmuxCmd("display-message", "-p", "-t", pane, liveScreenFormat, ";", "capture-pane", "-e", "-p", "-t", pane, ";", "display-message", "-p", "-t", pane, liveScreenFormat).Output()
	if err != nil {
		return screen, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) < 3 || lines[0] != lines[len(lines)-1] {
		return screen, fmt.Errorf("terminal moved during snapshot")
	}
	fields := strings.Split(lines[0], "|")
	if len(fields) != 8 {
		return screen, fmt.Errorf("invalid terminal state")
	}
	screen.PID = fields[0]
	values := make([]int, 7)
	for i := range values {
		values[i], err = strconv.Atoi(fields[i+1])
		if err != nil {
			return screen, err
		}
	}
	screen.Columns, screen.Rows, screen.X, screen.Y = values[0], values[1], values[2], values[3]
	if screen.Columns < 20 || screen.Columns > 1000 || screen.Rows < 8 || screen.Rows > 1000 || screen.X < 0 || screen.X >= screen.Columns || screen.Y < 0 || screen.Y >= screen.Rows {
		return screen, fmt.Errorf("terminal dimensions unsupported")
	}
	screen.Cursor, screen.Alternate, screen.Copy = values[4] == 1, values[5] == 1, values[6] != 0
	screen.ANSI = strings.Join(lines[1:len(lines)-1], "\n")
	return screen, nil
}

func (s *liveStore) interval(now time.Time, count int) time.Duration {
	if !s.fast || count >= 50000 {
		return remote.Interval(count)
	}
	for _, l := range s.sessions {
		if now.Before(l.until) && !l.written.IsZero() && now.Sub(l.written) < 2*time.Second {
			return 75 * time.Millisecond
		}
	}
	return remote.Interval(count)
}

func (s *liveStore) collect(route string, now time.Time, count int) *remote.Message {
	l := s.sessions[route]
	if l == nil {
		return nil
	}
	if !now.Before(l.until) {
		s.end(route, "expired")
		return nil
	}
	interval := remote.Interval(count)
	if s.fast && count < 50000 && !l.written.IsZero() && now.Sub(l.written) < 2*time.Second {
		interval = 75 * time.Millisecond
	}
	if !l.sent.IsZero() && now.Sub(l.sent) < interval {
		return nil
	}
	l.sent = now
	identity, foreground, err := livePaneState(l.pane)
	if err != nil || identity != l.identity {
		s.end(route, "target_changed")
		return &remote.Message{Type: "live_result", ID: l.token, Pane: l.pane, Status: "refused", Reason: "shell target changed or foreground program is unsupported"}
	}
	// Each write matures independently: continuous typing must not prevent older
	// inputs from becoming eligible for rejection. Capture starts after this time.
	for len(l.completions) > 0 && now.Sub(l.completions[0].at) >= liveSettle {
		l.echo = l.completions[0].seq
		l.completions = l.completions[1:]
	}

	screen, err := captureLiveScreen(l.pane)
	if err != nil {
		return nil
	}
	if !strings.HasPrefix(l.identity, screen.PID+":") {
		s.end(route, "target_changed")
		return &remote.Message{Type: "live_result", ID: l.token, Pane: l.pane, Status: "refused", Reason: "terminal target changed"}
	}
	screen.Typing, screen.Foreground = liveForeground[foreground], foreground
	screen.Accepted, screen.Echo = l.accepted, l.echo
	raw, _ := json.Marshal(screen)
	if string(raw) == l.previous {
		return nil
	}
	l.previous = string(raw)
	l.revision++
	return &remote.Message{Type: "live_screen", ID: l.token, Pane: l.pane, Revision: l.revision, Data: screen}
}
