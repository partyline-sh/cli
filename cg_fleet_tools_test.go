package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// The operate-partyline tools exist so an agent never has to reach outside them — the gap that made
// a dogfood run turn manual. Their failure modes are all "reported something the user cannot act on".

func TestMachineByNameIsForgivingButNeverAmbiguous(t *testing.T) {
	ms := []api.MachineOffer{{DaemonID: "d1", Machine: "MacBook-Air.local"}, {DaemonID: "d2", Machine: "monolith"}}

	if got, _ := machineByName(ms, "monolith"); got == nil || got.DaemonID != "d2" {
		t.Error("an exact name did not resolve")
	}
	// A human says "macbook", not "MacBook-Air.local". One partial match is not a guess.
	if got, _ := machineByName(ms, "macbook"); got == nil || got.DaemonID != "d1" {
		t.Error("an unambiguous partial name did not resolve")
	}
	// Two matches must REFUSE and name them, rather than pick one and start work on the wrong box.
	amb := []api.MachineOffer{{Machine: "build-1"}, {Machine: "build-2"}}
	got, reason := machineByName(amb, "build")
	if got != nil {
		t.Fatal("an ambiguous name resolved to a machine — that starts work somewhere nobody chose")
	}
	for _, want := range []string{"build-1", "build-2"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the refusal does not list %q, so the user cannot pick", want)
		}
	}
}

// Every refusal must carry the way forward. A bare "not found" costs the model a turn and the user a
// round trip.
func TestMachineRefusalsAreActionable(t *testing.T) {
	_, reason := machineByName(nil, "anything")
	if !strings.Contains(reason, "ptln setup") {
		t.Errorf("with no machines enrolled the reason never says how to enrol one: %q", reason)
	}
	_, reason = machineByName([]api.MachineOffer{{Machine: "mini"}}, "nope")
	if !strings.Contains(reason, "mini") {
		t.Errorf("the reason does not list the machines that DO exist: %q", reason)
	}
}

// THE GRANT MUST BE SPOKEN. Registering a directory lets a team's agents run code on someone's
// machine unattended. A bare "done" hides that, so the result always states it — and always states
// which run mode is in force, because that decides whether promoting starts work immediately.
func TestBindResultAlwaysStatesTheGrantAndTheRunMode(t *testing.T) {
	m := &api.MachineOffer{Machine: "mini", Online: true}

	auto := renderBindResult("add_machine_project", m, "widgets", false, "auto")
	if !strings.Contains(auto, "unattended") {
		t.Error("the result never says the directory can be built in unattended")
	}
	if !strings.Contains(auto, "remove-project") {
		t.Error("the result never says how to undo the grant")
	}
	if !strings.Contains(strings.ToLower(auto), "without asking") {
		t.Error("auto mode does not warn that promoted work starts immediately")
	}

	ask := renderBindResult("add_machine_project", m, "widgets", false, "ask")
	if !strings.Contains(strings.ToLower(ask), "waits for approval") {
		t.Error("ask mode does not say work waits for approval")
	}

	// Policy left unset must SAY so. Silence would read as "auto", which is a grant nobody made.
	none := renderBindResult("add_machine_project", m, "widgets", false, "")
	if !strings.Contains(none, "left as it was") {
		t.Error("an unset policy is not reported, so the user cannot tell what mode is in force")
	}

	// Offline is not a failure, but it IS the difference between "done" and "will happen later".
	off := renderBindResult("add_machine_project", &api.MachineOffer{Machine: "mini"}, "widgets", false, "auto")
	if !strings.Contains(off, "OFFLINE") {
		t.Error("an offline machine reads as if the work already happened")
	}
}

func TestSetRunModeExplainsWhatItChanged(t *testing.T) {
	m := &api.MachineOffer{Machine: "mini", Online: true}
	auto := renderBindResult("set_run_mode", m, "widgets", false, "auto")
	if !strings.Contains(auto, "UNATTENDED") || !strings.Contains(auto, "immediately") {
		t.Errorf("auto does not explain that promoting now starts work: %q", auto)
	}
	ask := renderBindResult("set_run_mode", m, "widgets", false, "ask")
	if !strings.Contains(ask, "QUEUE") {
		t.Errorf("ask does not explain that work queues: %q", ask)
	}
}

// A run-mode change must keep the project pointing at the SAME directory. Re-binding a different
// handle while "only changing the mode" would silently move where the team's work is built.
func TestRunModeChangeReusesTheDirectoryAlreadyInUse(t *testing.T) {
	m := &api.MachineOffer{Machine: "mini"}
	m.Repos = append(m.Repos, struct {
		Handle string `json:"handle"`
		Name   string `json:"name"`
		Parent string `json:"parent"`
	}{Handle: "abc123", Name: "widgets", Parent: "~/dev"})

	if got := existingHandleFor(m, "widgets"); got != "abc123" {
		t.Errorf("handle for the label = %q, want the one already advertised", got)
	}
	if got := existingHandleFor(m, "not-here"); got != "" {
		t.Error("invented a handle for a label the machine does not have — that would bind the wrong directory")
	}
}

// The board is read to decide what to do next, so the column that needs a human must be impossible
// to miss, and a long task must not wreck the shape of the list.
func TestBoardHighlightsWhatNeedsAHuman(t *testing.T) {
	b := &api.Board{
		Building: []api.BoardCard{{Task: strings.Repeat("a very long task title ", 20)}},
		Blocked:  []api.BoardCard{{Task: "failed thing"}},
	}
	out := renderBoard(b)
	if !strings.Contains(out, "BLOCKED") || !strings.Contains(out, "will not move on its own") {
		t.Error("the board does not call out the column that has stopped")
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 100 {
			t.Errorf("a row is %d chars — one long task destroys the board's shape: %q", len(line), line)
		}
	}
	// An empty board still has to say so rather than printing nothing.
	if !strings.Contains(renderBoard(&api.Board{}), "WORK BOARD") {
		t.Error("an empty board renders nothing at all")
	}
}

func TestMachineListTellsYouHowToEnrolWhenThereAreNone(t *testing.T) {
	out := renderMachines(nil)
	if !strings.Contains(out, "ptln setup") {
		t.Errorf("with no machines the list does not say how to get one: %q", out)
	}
}
