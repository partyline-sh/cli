package main

import (
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/remote"
)

const phoneA, phoneB = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"

func resizeMsg(pane string, data map[string]any) remote.Message {
	return remote.Message{Type: "resize", ID: "r", Pane: pane, Data: data}
}

func phoneSize(pane string, cols, rows int) remote.Message {
	return resizeMsg(pane, map[string]any{"mode": "phone", "columns": cols, "rows": rows})
}

func windowOption(t *testing.T, window, name string) string {
	t.Helper()
	out, _ := tmuxCmd("show-options", "-wqv", "-t", window, name).Output()
	return strings.TrimSpace(string(out))
}

func windowSize(t *testing.T, window string) string {
	t.Helper()
	out, _ := tmuxCmd("display-message", "-p", "-t", window, "#{window_width}x#{window_height}").Output()
	return strings.TrimSpace(string(out))
}

// Phone layout sizes the window, and desktop gives back exactly what it had: its size, and no
// window-size of its own, or the one the person set (a manual window keeps its 120x40).
func TestRemoteResizeAndRestore(t *testing.T) {
	tmuxTestServer(t, "ptln-remote-resize-test")
	s := &resizeStore{leases: map[string]*resizeLease{}}
	now := time.Now()
	plain := openTagged(t, Spec{Key: "plain", Label: "Plain"})
	set := openTagged(t, Spec{Key: "set", Label: "Set"})
	_ = tmuxCmd("set-option", "-w", "-t", windowOf(t, set), "window-size", "smallest").Run()
	manual := openTagged(t, Spec{Key: "manual", Label: "Manual"})
	_ = tmuxCmd("resize-window", "-t", windowOf(t, manual), "-x", "120", "-y", "40").Run()
	for pane, want := range map[string]string{plain: "", set: "smallest", manual: "manual"} {
		w := windowOf(t, pane)
		before := windowSize(t, w)
		if st, why := s.handle(phoneA, phoneSize(pane, 44, 30), now); st != "ok" {
			t.Fatalf("phone layout refused: %s", why)
		}
		if got := windowSize(t, w); got != "44x30" {
			t.Errorf("window is %s, want 44x30", got)
		}
		if st, why := s.handle(phoneA, resizeMsg(pane, map[string]any{"mode": "desktop"}), now); st != "ok" {
			t.Fatalf("desktop refused: %s", why)
		}
		if got := windowOption(t, w, "window-size"); got != want {
			t.Errorf("window-size after release is %q, want %q", got, want)
		}
		if got := windowSize(t, w); got != before {
			t.Errorf("window is %s after release, want %s", got, before)
		}
		if got := windowOption(t, w, remoteResizeRestore); got != "" {
			t.Errorf("restore record left behind: %q", got)
		}
	}
}

func TestRemoteResizeRefusals(t *testing.T) {
	tmuxTestServer(t, "ptln-remote-resize-refuse")
	s := &resizeStore{leases: map[string]*resizeLease{}}
	now := time.Now()
	pane := openTagged(t, Spec{Key: "one", Label: "One"})
	split := openTagged(t, Spec{Key: "two", Label: "Two"})
	_ = tmuxCmd("split-window", "-d", "-t", split, "sleep", "300").Run()
	for name, m := range map[string]remote.Message{
		"split window": phoneSize(split, 44, 30),
		"unknown pane": phoneSize("%999", 44, 30),
		"too narrow":   phoneSize(pane, 10, 30),
		"too tall":     phoneSize(pane, 44, 500),
		"no mode":      resizeMsg(pane, map[string]any{}),
		"tmux target":  phoneSize("ptln:0", 44, 30),
	} {
		if st, _ := s.handle(phoneA, m, now); st == "ok" {
			t.Errorf("%s accepted", name)
		}
	}
	if st, why := s.handle(phoneA, phoneSize(pane, 44, 30), now); st != "ok" {
		t.Fatalf("refused: %s", why)
	}
	if st, _ := s.handle(phoneB, phoneSize(pane, 50, 30), now); st == "ok" {
		t.Error("a second phone took a held window")
	}
	if st, _ := s.handle(phoneB, resizeMsg(pane, map[string]any{"mode": "desktop"}), now); st == "ok" {
		t.Error("a second phone released a window it does not hold")
	}
}

// A window goes back when its phone stops repeating, disconnects, or a previous run crashed holding it.
func TestRemoteResizeLeaseEnds(t *testing.T) {
	tmuxTestServer(t, "ptln-remote-resize-lease")
	now := time.Now()
	pane := openTagged(t, Spec{Key: "lease", Label: "Lease"})
	w := windowOf(t, pane)
	released := func(what string) {
		t.Helper()
		if got := windowOption(t, w, "window-size"); got != "" {
			t.Errorf("%s: window-size still %q", what, got)
		}
	}
	s := &resizeStore{leases: map[string]*resizeLease{}}
	s.handle(phoneA, phoneSize(pane, 44, 30), now)
	s.tick(now.Add(remoteResizeLease / 2))
	if windowOption(t, w, "window-size") != "manual" {
		t.Fatal("released while the lease was live")
	}
	s.tick(now.Add(remoteResizeLease + time.Second))
	released("lease expired")

	s.handle(phoneA, phoneSize(pane, 44, 30), now)
	s.releaseAll(phoneA)
	released("phone disconnected")

	_ = tmuxCmd("resize-window", "-t", w, "-x", "120", "-y", "40").Run() // the person's own manual size
	s.handle(phoneA, phoneSize(pane, 44, 30), now)                       // this run "crashes" holding it
	fresh := &resizeStore{leases: map[string]*resizeLease{}}
	fresh.tick(now)
	if got := windowSize(t, w); got != "120x40" || windowOption(t, w, "window-size") != "manual" {
		t.Errorf("restarted after a crash: %s, window-size %q", got, windowOption(t, w, "window-size"))
	}
}

// A restore tmux refuses keeps the lease and the record, so the next tick tries again and no other
// phone takes the window meanwhile.
func TestRemoteResizeFailedRestoreRetries(t *testing.T) {
	tmuxTestServer(t, "ptln-remote-resize-retry")
	now := time.Now()
	pane := openTagged(t, Spec{Key: "retry", Label: "Retry"})
	w := windowOf(t, pane)
	before := windowSize(t, w)
	s := &resizeStore{leases: map[string]*resizeLease{}}
	s.handle(phoneA, phoneSize(pane, 44, 30), now)
	_ = tmuxCmd("set-option", "-w", "-t", w, remoteResizeRestore, "inherited 0x0").Run() // resize-window refuses 0x0
	s.tick(now.Add(remoteResizeLease + time.Second))
	if s.leases[w] == nil || windowOption(t, w, remoteResizeRestore) == "" {
		t.Fatal("a failed restore dropped the lease or the record")
	}
	if st, _ := s.handle(phoneB, phoneSize(pane, 50, 30), now); st == "ok" {
		t.Error("another phone took a window still being restored")
	}
	_ = tmuxCmd("set-option", "-w", "-t", w, remoteResizeRestore, "inherited "+before).Run()
	s.tick(now.Add(remoteResizeLease + 2*time.Second))
	if s.leases[w] != nil || windowSize(t, w) != before {
		t.Errorf("retry did not restore: lease %v, size %s", s.leases[w], windowSize(t, w))
	}
}
