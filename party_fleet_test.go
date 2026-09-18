package main

import (
	"regexp"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// fleet_read is the machine-side twin of backlog_read: an agent that can see the work but not the
// machines still can't answer "where should this run" or "why is nothing building". The rendering
// has two jobs, and the second one is the load-bearing one:
//
//  1. name every machine the caller may see, with reachability, version, labels and health;
//  2. say NOTHING about a metric that was never reported. A zero here is a confident lie.

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }

func fullNode() api.FleetNodeInfo {
	return api.FleetNodeInfo{
		ID: "node-here", Name: "monolith", Owner: "darcy", Online: true, Status: "online",
		LastSeenS: iptr(12), Version: "0.26.7",
		Projects: []string{"partyline", "acr-cloud"},
		Building: &api.FleetBuilding{Task: "wire the fleet read", Project: "partyline", Count: 3},
		Load:     &api.FleetLoad{Load1: f64(2.4), Cores: iptr(10)},
		Memory:   &api.FleetMemory{UsedPct: f64(61), TotalMB: f64(32768)},
		Disk:     &api.FleetDisk{UsedPct: f64(82), FreeGB: f64(41), Volume: "Data"},
		Sessions: &api.FleetSessions{Live: 3, Busy: 1, Engines: []string{"claude opus", "codex"}},
	}
}

func TestFormatFleetReportsEveryMachineAndItsHealth(t *testing.T) {
	out := formatFleet(&api.Fleet{Nodes: []api.FleetNodeInfo{fullNode()}, Count: 1}, "")
	for _, want := range []string{
		"monolith", "@darcy", "online", "12s ago", "ptln 0.26.7",
		"partyline, acr-cloud",
		"wire the fleet read", "and 2 more run(s)", // 3 building here, 1 named
		"load 2.4 across 10 cores",
		"memory 61% used of 32 GB",
		"disk 82% used, 41 GB free on Data",
		"local AI sessions: 3, 1 busy", "claude opus, codex",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q from:\n%s", want, out)
		}
	}
}

// THE ABSENCE RULE. A machine that has never reported metrics must render with no numbers at all —
// not "0%", not "0 GB", not "0 cores". An agent reading a fabricated zero would conclude the machine
// is idle and empty, which is the exact inversion of "we don't know".
func TestFormatFleetNeverInventsAReadingForAnUnreportedMetric(t *testing.T) {
	quiet := api.FleetNodeInfo{ID: "n2", Name: "mini-6.local", Owner: "sam", Status: "offline"}
	out := formatFleet(&api.Fleet{Nodes: []api.FleetNodeInfo{quiet}, Count: 1}, "")

	if !strings.Contains(out, "no health reported") {
		t.Errorf("silence was not stated as silence:\n%s", out)
	}
	if !strings.Contains(out, "last heartbeat never") {
		t.Errorf("a node never seen must not read as recently seen:\n%s", out)
	}
	// The strongest form of the rule: strip the node's own NAME (which contains a digit) and no
	// digit may remain on its lines at all — it reported nothing measurable, so there is nothing
	// numeric to say about it.
	body := strings.ReplaceAll(out[strings.Index(out, "mini-6.local"):], "mini-6.local", "")
	if m := regexp.MustCompile(`\d`).FindString(body); m != "" {
		t.Errorf("a number (%q) leaked onto a node that reported none:\n%s", m, body)
	}
}

// Partial reports are the common case on a mixed-version fleet: one field landed, the rest didn't.
// Each half must stand alone, and load without a core count must say it cannot be judged rather than
// implying a verdict.
func TestFormatFleetRendersPartialReportsWithoutFillingGaps(t *testing.T) {
	n := api.FleetNodeInfo{
		ID: "n3", Name: "half", Status: "idle", LastSeenS: iptr(600),
		Load:   &api.FleetLoad{Load1: f64(3)},
		Memory: &api.FleetMemory{UsedPct: f64(50)},
		Disk:   &api.FleetDisk{FreeGB: f64(9.5)},
	}
	out := formatFleet(&api.Fleet{Nodes: []api.FleetNodeInfo{n}, Count: 1}, "")
	if !strings.Contains(out, "core count not reported") {
		t.Errorf("load rendered without flagging that it cannot be judged:\n%s", out)
	}
	for _, forbidden := range []string{"of 0 GB", "memory 0%", "disk 0%", "across 0 cores", "0 GB free", "sessions"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("filled a gap with a zero (%q):\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "memory 50% used") || strings.Contains(out, "memory 50% used of") {
		t.Errorf("invented a total for a memory report that had none:\n%s", out)
	}
	if !strings.Contains(out, "disk 9.5 GB free") {
		t.Errorf("dropped the one disk number that was reported:\n%s", out)
	}
	if !strings.Contains(out, "10m ago") {
		t.Errorf("heartbeat age not rendered:\n%s", out)
	}
}

// "Here" vs "elsewhere" is the difference between "I can check that myself" and "that's someone
// else's laptop". It is resolved LOCALLY, so an empty local id marks nothing rather than guessing.
func TestFormatFleetMarksThisMachineOnlyWhenItKnowsWhichOneItIs(t *testing.T) {
	nodes := []api.FleetNodeInfo{fullNode(), {ID: "n2", Name: "mini-6.local", Status: "online", LastSeenS: iptr(5)}}
	marked := formatFleet(&api.Fleet{Nodes: nodes, Count: 2}, "node-here")
	if !strings.Contains(marked, "THIS MACHINE") {
		t.Errorf("own machine not marked:\n%s", marked)
	}
	if strings.Count(marked, "THIS MACHINE") != 1 {
		t.Errorf("marked more than one machine as here:\n%s", marked)
	}
	if i, j := strings.Index(marked, "THIS MACHINE"), strings.Index(marked, "mini-6.local"); i > j {
		t.Errorf("marked the wrong machine:\n%s", marked)
	}
	if strings.Contains(formatFleet(&api.Fleet{Nodes: nodes, Count: 2}, ""), "THIS MACHINE") {
		t.Error("marked a machine as here without a local device id to match against")
	}
}

// An empty fleet must read as empty and say what to do about it — an agent that reads nothing
// concludes the tool is broken and hands the question back.
func TestFormatFleetEmptyAndNil(t *testing.T) {
	if got := formatFleet(&api.Fleet{}, ""); !strings.Contains(got, "daemon enable") {
		t.Errorf("empty fleet did not teach the next step: %q", got)
	}
	if got := formatFleet(nil, ""); got == "" {
		t.Error("nil fleet produced no message at all")
	}
}

func TestFleetReadIsAdvertisedAsATool(t *testing.T) {
	for _, tl := range toolDefs {
		if tl["name"] == "fleet_read" {
			return
		}
	}
	t.Error("fleet_read is not in toolDefs, so no agent will ever call it")
}
