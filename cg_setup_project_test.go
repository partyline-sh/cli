package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// offer builds a machine as /api/v1/daemon/candidates reports it.
func offer(name string, online bool, repos [][2]string, dests [][2]string) api.MachineOffer {
	m := api.MachineOffer{DaemonID: "d-" + name, Machine: name, Online: online}
	for _, r := range repos {
		m.Repos = append(m.Repos, struct {
			Handle string `json:"handle"`
			Name   string `json:"name"`
			Parent string `json:"parent"`
		}{Handle: r[0], Name: r[1], Parent: "/repos"})
	}
	for _, d := range dests {
		m.Destinations = append(m.Destinations, struct {
			Handle string `json:"handle"`
			Parent string `json:"parent"`
			Label  string `json:"label"`
		}{Handle: d[0], Parent: d[1], Label: "dest"})
	}
	return m
}

// A machine that already has the checkout binds instantly; one that must clone takes minutes. The
// picker exists to make that difference visible, so the classification has to be right.
func TestSetupCandidatesPrefersAnExistingCheckout(t *testing.T) {
	machines := []api.MachineOffer{
		offer("monolith", true, [][2]string{{"h1", "landsearch"}}, [][2]string{{"d1", "/mnt/data"}}),
	}
	got := setupCandidates(machines, "landsearch", "")
	if len(got) != 1 {
		t.Fatalf("got %d candidates", len(got))
	}
	if !got[0].Instant() {
		t.Fatal("a machine with a matching checkout must bind instantly, not clone")
	}
	if got[0].Handle != "h1" || got[0].DestinationHandle != "" {
		t.Fatalf("candidate = %+v, want the repo handle and no destination", got[0])
	}
}

func TestSetupCandidatesFallsBackToCloning(t *testing.T) {
	machines := []api.MachineOffer{
		offer("box", true, [][2]string{{"h1", "something-else"}}, [][2]string{{"d1", "/srv"}}),
	}
	got := setupCandidates(machines, "landsearch", "")
	if len(got) != 1 || got[0].Instant() {
		t.Fatalf("candidate = %+v, want a clone offer", got)
	}
	if got[0].DestinationHandle != "d1" || got[0].Parent != "/srv" {
		t.Fatalf("candidate = %+v, want the destination handle and its parent", got[0])
	}
}

// A machine with nothing to offer is not a choice — listing it would invite picking something that
// cannot work.
func TestSetupCandidatesSkipsMachinesWithNothingToOffer(t *testing.T) {
	machines := []api.MachineOffer{offer("bare", true, nil, nil)}
	if got := setupCandidates(machines, "x", ""); len(got) != 0 {
		t.Fatalf("got %+v, want no candidates", got)
	}
}

// Order is the recommendation: online-and-instant first, so the machine that can be working in a
// second is the one the operator reads first.
func TestSetupCandidatesOrdersByUsefulness(t *testing.T) {
	machines := []api.MachineOffer{
		offer("zeta-clone", true, nil, [][2]string{{"d1", "/srv"}}),
		offer("offline-box", false, [][2]string{{"h0", "proj"}}, nil),
		offer("alpha-has-it", true, [][2]string{{"h1", "proj"}}, nil),
	}
	got := setupCandidates(machines, "proj", "")
	want := []string{"alpha-has-it", "zeta-clone", "offline-box"}
	for i, w := range want {
		if got[i].Machine != w {
			t.Fatalf("position %d = %q, want %q (order: online+instant, then online, then offline)", i, got[i].Machine, w)
		}
	}
}

// The question turn has one job: make the grant explicit and forbid choosing on the operator's
// behalf. If that language goes missing, a model will quietly enrol every box.
func TestChoicesAskRatherThanAssume(t *testing.T) {
	out := renderSetupChoices("proj", []setupNodeChoice{
		{Machine: "monolith", Handle: "h1", Online: true},
		{Machine: "laptop", DestinationHandle: "d1", Parent: "/srv", Online: true},
	})
	for _, want := range []string{"ASK THE OPERATOR", "do not default to all", "GRANT", "unattended"} {
		if !strings.Contains(out, want) {
			t.Errorf("the question is missing %q", want)
		}
	}
	if !strings.Contains(out, "already has this checkout") || !strings.Contains(out, "takes a few minutes") {
		t.Error("the operator must be able to see which machines are instant and which clone")
	}
}

// With no candidates the answer has to be a next step, not a dead end.
func TestChoicesWithNoMachinesTeachTheFix(t *testing.T) {
	out := renderSetupChoices("proj", nil)
	if !strings.Contains(out, "daemon add-project") || !strings.Contains(out, "scan-root add") {
		t.Fatalf("empty fleet must name both ways to fix it, got:\n%s", out)
	}
}

// The operator answers with names, because names are what they were shown.
func TestChooseSetupNodesMatchesOnName(t *testing.T) {
	nodes := []setupNodeChoice{
		{Machine: "monolith"}, {Machine: "MacBook-Air.local"},
	}
	chosen, unknown := chooseSetupNodes(nodes, []string{"monolith", "macbook-air", "  "})
	if len(chosen) != 2 {
		t.Fatalf("chose %d, want both (case-insensitive, short hostname allowed)", len(chosen))
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown = %v", unknown)
	}
}

// A name that matches nothing must be REPORTED. Silently enrolling nothing is how someone believes
// a machine is set up when it is not.
func TestChooseSetupNodesReportsUnknownNames(t *testing.T) {
	_, unknown := chooseSetupNodes([]setupNodeChoice{{Machine: "monolith"}}, []string{"monolith", "typo-box"})
	if len(unknown) != 1 || unknown[0] != "typo-box" {
		t.Fatalf("unknown = %v, want the unmatched name", unknown)
	}
}

// The outcome turn must state the grant and hand the session on to the description interview —
// that is what makes "one request" cover the description too.
func TestOutcomeStatesTheGrantAndTheNextStep(t *testing.T) {
	set := &projectSetup{Label: "proj", Thread: "t-1", Path: "/repo"}
	out := renderSetupOutcome(set, []setupBindResult{
		{Machine: "monolith", State: "ready"},
		{Machine: "laptop", State: "cloning", Reason: "a clone is running"},
	})

	if !strings.Contains(out, "GRANT") || !strings.Contains(out, "unattended") {
		t.Error("the outcome must say what was granted")
	}
	if !strings.Contains(out, "interview") || !strings.Contains(out, "planning_open") {
		t.Error("the outcome must hand the session on to the description interview")
	}
	if !strings.Contains(out, "1 machine(s) can build this now") {
		t.Errorf("the outcome must count what is actually runnable, got:\n%s", out)
	}
	if !strings.Contains(out, ".partyline.json") {
		t.Error("the pin is a file teammates need checked in — say so")
	}
}

// An outcome with nothing ready must not claim anything is.
func TestOutcomeWithNothingReadyClaimsNothing(t *testing.T) {
	set := &projectSetup{Label: "proj", Thread: "t", Path: "/repo"}
	out := renderSetupOutcome(set, []setupBindResult{{Machine: "box", State: "failed", Reason: "no"}})
	if strings.Contains(out, "can build this now") {
		t.Fatal("nothing succeeded — the summary must not say work will dispatch")
	}
	if !strings.Contains(out, "failed") {
		t.Fatal("a failure must be reported")
	}
}

// A uuid goes straight through; a label is resolved. This is the bug where `project ls` printed a
// label, `ptln help` documented `show <label>`, and only a uuid worked.
func TestLooksLikeUUID(t *testing.T) {
	for in, want := range map[string]bool{
		"4659f9a2-f9a8-403e-a186-65a6f674950c": true,
		"landsearch":                           false,
		"":                                     false,
		"4659f9a2f9a8403ea18665a6f674950c":     false,
		"zzzzzzzz-f9a8-403e-a186-65a6f674950c": false,
	} {
		if got := looksLikeUUID(in); got != want {
			t.Errorf("looksLikeUUID(%q) = %v, want %v", in, got, want)
		}
	}
}

// The CLI answer is numbers, because numbers are what it printed. Anything out of range, repeated
// or unparseable is IGNORED rather than guessed at: enabling the wrong machine is a grant given by
// accident, and a fat-fingered "12" must not become machine 1 and machine 2.
func TestPickNodesByNumber(t *testing.T) {
	nodes := []setupNodeChoice{{Machine: "a"}, {Machine: "b"}, {Machine: "c"}}

	got := pickNodesByNumber(nodes, "1, 3\n")
	if len(got) != 2 || got[0].Machine != "a" || got[1].Machine != "c" {
		t.Fatalf("got %+v, want a and c", got)
	}
	if n := pickNodesByNumber(nodes, "\n"); len(n) != 0 {
		t.Fatalf("an empty answer must select nothing, got %+v", n)
	}
	if n := pickNodesByNumber(nodes, "0, 9, banana"); len(n) != 0 {
		t.Fatalf("out-of-range and junk must be ignored, got %+v", n)
	}
	if n := pickNodesByNumber(nodes, "2 2 2"); len(n) != 1 {
		t.Fatalf("a repeated number must enable one machine, got %+v", n)
	}
}
