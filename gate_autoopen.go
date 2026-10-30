package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The approval popup opens by itself (docs/plans/bubbletea-ui.md §2, reversed at the user's request
// on 2026-10-27): the watcher opens it when something waits for the person and they have stopped
// typing. Every decision in it needs the code, so a stray key cannot decide.

const (
	gateIdleFor   = 3 * time.Second  // no key from the client for this long counts as idle
	gateSnoozeFor = 10 * time.Minute // esc keeps it closed this long, unless something new waits
)

func gatePIDPath() string    { return filepath.Join(stateDir(), "gate.pid") }
func gateSnoozePath() string { return filepath.Join(stateDir(), "gate-snooze.json") }

// markGateOpen records that an approval popup is running, for the watcher; the returned func
// removes the mark when the popup exits.
func markGateOpen() func() {
	_ = os.WriteFile(gatePIDPath(), []byte(strconv.Itoa(os.Getpid())), 0o600)
	return func() { _ = os.Remove(gatePIDPath()) }
}

// gateOpen: an approval popup is running now.
func gateOpen() bool {
	b, err := os.ReadFile(gatePIDPath())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == nil
}

// waitingSig names the set of waiting items: a new item, or a new revision of one, changes it.
func waitingSig(runs []*loopRun) string {
	var parts []string
	for _, r := range runs {
		if loopWaiting(r) && r.Remote == nil {
			parts = append(parts, fmt.Sprintf("%s:%s:%d", r.ID, r.State, r.Revision))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

type gateSnoozeMark struct {
	Until time.Time `json:"until"`
	Sig   string    `json:"sig"`
}

// gateSnooze is esc or q in the popup: do not open it again for these items for a while.
func gateSnooze(runs []*loopRun) {
	b, _ := json.Marshal(gateSnoozeMark{Until: time.Now().Add(gateSnoozeFor), Sig: waitingSig(runs)})
	_ = os.WriteFile(gateSnoozePath(), b, 0o600)
}

func gateSnoozed(sig string, now time.Time) bool {
	var s gateSnoozeMark
	b, err := os.ReadFile(gateSnoozePath())
	if err != nil || json.Unmarshal(b, &s) != nil {
		return false
	}
	return s.Sig == sig && now.Before(s.Until)
}

type gateClient struct {
	Name          string
	Activity      time.Time
	Width, Height int
}

// gateOpener opens the popup from the watcher's tick; its tmux calls are variables in tests.
type gateOpener struct {
	waiting func() []*loopRun
	clients func() ([]gateClient, error)
	open    func(c gateClient) error
}

func newGateOpener() *gateOpener {
	return &gateOpener{waiting: loopWaitingRuns, clients: tmuxClients, open: openGateOn}
}

// tick opens the popup when something waits, none is open, it is not snoozed for these items, and
// the one attached client has been idle. A popup tmux dropped on a busy client leaves no gate.pid,
// so the next tick tries again.
func (o *gateOpener) tick(now time.Time) bool {
	runs := o.waiting()
	sig := waitingSig(runs)
	// tmux shows one popup per client: opening this one would close an open chat popup.
	if sig == "" || gateOpen() || chatPopupOpen() || gateSnoozed(sig, now) {
		return false
	}
	cs, err := o.clients()
	if err != nil || len(cs) != 1 { // with two clients the gate refuses anyway
		return false
	}
	if now.Sub(cs[0].Activity) < gateIdleFor {
		return false
	}
	return o.open(cs[0]) == nil
}

// tmuxClients lists the attached clients and when each last had a key.
func tmuxClients() ([]gateClient, error) {
	out, err := tmuxCmd("list-clients", "-F", "#{client_name}\t#{client_activity}\t#{client_width}\t#{client_height}").Output()
	if err != nil {
		return nil, err
	}
	var cs []gateClient
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 4 {
			continue
		}
		// An activity time that does not parse reads as "active now": idle is the condition for
		// opening a popup over someone, so not knowing must never look idle.
		activity := time.Now()
		if secs, err := strconv.ParseInt(f[1], 10, 64); err == nil {
			activity = time.Unix(secs, 0)
		}
		w, _ := strconv.Atoi(f[2])
		h, _ := strconv.Atoi(f[3])
		cs = append(cs, gateClient{Name: f[0], Activity: activity, Width: w, Height: h})
	}
	return cs, nil
}

// openGateOn opens the popup on client without waiting: display-popup returns only when the popup
// closes, and the watcher's tick must not wait for the person.
func openGateOn(c gateClient) error {
	cmd := tmuxCmd(gatePopupOn(c, selfExe())...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// gatePopupOn is the popup's command line for one client, sized to it.
func gatePopupOn(c gateClient, self string) []string {
	w, h := gatePopupSize(c.Width, c.Height)
	return []string{"display-popup", "-c", c.Name, "-E", "-w", strconv.Itoa(w), "-h", strconv.Itoa(h), "-T", " approvals ", self, "loop", "gate"}
}
