package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/remote"
)

// A phone may ask for a session's window at its own size, so an agent's screen is laid out for the
// phone instead of shrunk. Only when the person turns Phone layout on: the window is the same one
// the Mac shows, so it narrows there too until it is released. Agreed with #13 codex:
// resize {pane, data:{mode:"phone",columns,rows}} or {mode:"desktop"}, replying input_result.
//
// One phone holds a window at a time, and only a window with a single pane (resizing one pane of a
// split squeezes its neighbours). The phone repeats the request to keep it; the window goes back
// when it asks for desktop, disconnects, or stops repeating for remoteResizeLease. What the window
// had before (its window-size and its size) is kept in a window option, so a crash still restores it
// the next time this runs, and a failed restore is tried again on the next tick.

const (
	remoteResizeLease   = 30 * time.Second
	remoteResizeRestore = "@ptln_phone_restore" // "inherited 120x40", or "local:<its own window-size> 120x40"
)

type resizeLease struct {
	phone  string
	window string
	until  time.Time
}

type resizeStore struct {
	mu     sync.Mutex
	leases map[string]*resizeLease // window id → lease
	stale  sync.Once
}

var processResizes = &resizeStore{leases: map[string]*resizeLease{}}

// handle runs one resize command for phone route, already authorized as a known phone.
func (s *resizeStore) handle(route string, m remote.Message, now time.Time) (string, string) {
	var req struct {
		Mode    string `json:"mode"`
		Columns int    `json:"columns"`
		Rows    int    `json:"rows"`
	}
	if b, err := json.Marshal(m.Data); err != nil || json.Unmarshal(b, &req) != nil {
		return "refused", "invalid resize"
	}
	window, err := resizeWindowOf(m.Pane)
	if err != nil {
		return "refused", err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(now)
	l := s.leases[window]
	if l != nil && l.phone != route {
		return "refused", "another phone has this window at its size"
	}
	switch req.Mode {
	case "desktop":
		if l != nil {
			s.release(l)
		}
		return "ok", ""
	case "phone":
		if req.Columns < 20 || req.Columns > 300 || req.Rows < 5 || req.Rows > 200 {
			return "refused", "size must be 20–300 columns by 5–200 rows"
		}
		if l == nil {
			if err := resizeRemember(window); err != nil {
				return "refused", err.Error()
			}
			l = &resizeLease{phone: route, window: window}
			s.leases[window] = l
		}
		l.until = now.Add(remoteResizeLease)
		if err := tmuxCmd("resize-window", "-t", window, "-x", fmt.Sprint(req.Columns), "-y", fmt.Sprint(req.Rows)).Run(); err != nil {
			s.release(l)
			return "refused", "tmux could not resize the window"
		}
		return "ok", ""
	}
	return "refused", "mode must be phone or desktop"
}

// releaseAll gives back every window a phone holds, when its session ends.
func (s *resizeStore) releaseAll(route string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.leases {
		if l.phone == route {
			s.release(l)
		}
	}
}

// tick expires leases a phone stopped repeating, and restores windows a crashed run left behind.
func (s *resizeStore) tick(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire(now)
}

func (s *resizeStore) expire(now time.Time) {
	s.stale.Do(resizeRestoreStale)
	for _, l := range s.leases {
		if now.After(l.until) {
			s.release(l)
		}
	}
}

// release gives the window back. If tmux refuses, the lease stays, expired, so no other phone takes
// the window and the next tick tries again; a window that no longer exists is simply forgotten.
func (s *resizeStore) release(l *resizeLease) {
	l.until = time.Time{}
	if err := resizeRestore(l.window); err != nil && tmuxCmd("display-message", "-p", "-t", l.window, "").Run() == nil {
		return
	}
	delete(s.leases, l.window)
}

// resizeWindowOf is the window of a partyline pane that is alone in its window.
func resizeWindowOf(pane string) (string, error) {
	inventory, err := remoteSnapshot("sessions", "focus")
	if err != nil {
		return "", err
	}
	for _, row := range inventory.([]map[string]string) {
		if row["pane"] != pane {
			continue
		}
		out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_panes}").Output()
		if err != nil {
			return "", errors.New("pane unavailable")
		}
		if strings.TrimSpace(string(out)) != "1" {
			return "", errors.New("this window is split; only a window with one pane can be resized")
		}
		return row["window"], nil
	}
	return "", errors.New("pane unavailable")
}

// resizeRemember records the window's own window-size, if it has one, before the phone changes it.
func resizeRemember(window string) error {
	if out, _ := tmuxCmd("show-options", "-wqv", "-t", window, remoteResizeRestore).Output(); strings.TrimSpace(string(out)) != "" {
		return nil // already recorded by an earlier run that did not restore it
	}
	own, err := tmuxCmd("show-options", "-wqv", "-t", window, "window-size").Output()
	if err != nil {
		return errors.New("pane unavailable")
	}
	size, err := tmuxCmd("display-message", "-p", "-t", window, "#{window_width}x#{window_height}").Output()
	if err != nil {
		return errors.New("pane unavailable")
	}
	saved := "inherited"
	if v := strings.TrimSpace(string(own)); v != "" {
		saved = "local:" + v
	}
	return tmuxCmd("set-option", "-w", "-t", window, remoteResizeRestore, saved+" "+strings.TrimSpace(string(size))).Run()
}

// resizeRestore puts the window back at its size and window-size. The size first: a manual window,
// or one no client is attached to, would otherwise stay at the phone's. Then tmux sizes an inherited
// window to the Mac's client again. The record goes only once both have worked.
func resizeRestore(window string) error {
	out, err := tmuxCmd("show-options", "-wqv", "-t", window, remoteResizeRestore).Output()
	if err != nil {
		return err
	}
	saved := strings.Fields(strings.TrimSpace(string(out)))
	if len(saved) == 0 {
		return nil
	}
	if len(saved) == 2 {
		if w, h, ok := strings.Cut(saved[1], "x"); ok {
			if err := tmuxCmd("resize-window", "-t", window, "-x", w, "-y", h).Run(); err != nil {
				return err
			}
		}
	}
	if v, ok := strings.CutPrefix(saved[0], "local:"); ok {
		err = tmuxCmd("set-option", "-w", "-t", window, "window-size", v).Run()
	} else {
		err = tmuxCmd("set-option", "-wu", "-t", window, "window-size").Run()
	}
	if err != nil {
		return err
	}
	return tmuxCmd("set-option", "-wu", "-t", window, remoteResizeRestore).Run()
}

// resizeRestoreStale restores windows a previous run held when it exited.
func resizeRestoreStale() {
	out, err := tmuxCmd("list-windows", "-a", "-F", "#{window_id}\t#{"+remoteResizeRestore+"}").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id, saved, ok := strings.Cut(line, "\t"); ok && saved != "" {
			_ = resizeRestore(id)
		}
	}
}
