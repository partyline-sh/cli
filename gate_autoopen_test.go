package main

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// The watcher opens the popup on the one idle client when something waits; not while the person
// types, not while one is open, not again for the same items after esc, and not with two clients.
// A popup tmux dropped (no gate.pid appeared) is tried again on the next tick.
func TestGateAutoOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	r := waitingRun(t, loopAwaiting)
	waiting := []*loopRun{r}
	clients := []gateClient{{Name: "/dev/ttys004", Activity: now.Add(-10 * time.Second)}}
	var opened []string
	o := &gateOpener{
		waiting: func() []*loopRun { return waiting },
		clients: func() ([]gateClient, error) { return clients, nil },
		open:    func(c gateClient) error { opened = append(opened, c.Name); return nil },
	}
	if !o.tick(now) || len(opened) != 1 || opened[0] != "/dev/ttys004" {
		t.Fatalf("idle client with a waiting run: opened %v", opened)
	}
	// tmux dropped it: no gate.pid, so the next tick tries again.
	if !o.tick(now) || len(opened) != 2 {
		t.Errorf("a dropped popup was not retried: %v", opened)
	}
	// One is open now.
	_ = os.WriteFile(gatePIDPath(), []byte(strconv.Itoa(os.Getpid())), 0o600)
	if o.tick(now) {
		t.Error("opened a second popup while one is open")
	}
	_ = os.Remove(gatePIDPath())
	// The person is typing.
	clients[0].Activity = now.Add(-time.Second)
	if o.tick(now) {
		t.Error("opened while the client was active")
	}
	clients[0].Activity = now.Add(-10 * time.Second)
	// esc snoozes these items; a new item opens it at once.
	gateSnooze(waiting)
	if o.tick(now) {
		t.Error("opened again for snoozed items")
	}
	if !o.tick(now.Add(gateSnoozeFor + time.Second)) {
		t.Error("still snoozed after the snooze ran out")
	}
	waiting = append(waiting, waitingRun(t, loopAwaitYou))
	if !o.tick(now) {
		t.Error("a new item did not open it through the snooze")
	}
	// Two clients: the gate would refuse, so it does not open.
	clients = append(clients, gateClient{Name: "/dev/ttys005", Activity: now.Add(-time.Minute)})
	if o.tick(now) {
		t.Error("opened with two clients attached")
	}
	// Nothing waiting.
	clients, waiting = clients[:1], nil
	if o.tick(now) {
		t.Error("opened with nothing waiting")
	}
	if got := gatePopupOn(gateClient{Name: "/dev/ttys004", Width: 80, Height: 24}, "ptln"); got[0] != "display-popup" || got[1] != "-c" || got[2] != "/dev/ttys004" || got[len(got)-1] != "gate" {
		t.Errorf("popup command: %q", got)
	}
	// The popup fits its client: tmux refuses one taller or wider and shows nothing.
	for _, c := range []struct{ cols, rows, w, h int }{{200, 60, 96, 30}, {80, 24, 78, 22}, {0, 0, 96, 30}, {10, 5, 8, 3}} {
		if w, h := gatePopupSize(c.cols, c.rows); w != c.w || h != c.h {
			t.Errorf("%dx%d client: popup %dx%d, want %dx%d", c.cols, c.rows, w, h, c.w, c.h)
		}
	}
}
