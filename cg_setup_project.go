package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// cg_setup_project.go — "make this a partyline project", as one request.
//
// Every piece of this already existed: create_project set the repo up, list_machines said what each
// box could be pointed at, add_machine_project bound one. What did not exist was the ARC. An agent
// called create_project, got "registered the directory on this machine", reported success and
// stopped — and the operator found out later that none of their other machines could run anything.
// Three correct tools, and the shape of the job was invisible in all of them.
//
// So this is one tool with two turns, which is what a request needs when the answer is the
// operator's to give:
//
//	turn 1 (no machines) — set the repo up, then RETURN THE CANDIDATES as a question
//	turn 2 (machines)    — bind each, wait for it to land, and say what is runnable where
//
// From the operator's side that is one ask and one answer. From the model's side it is a tool that
// cannot be called wrong, because the first call tells it what the second one needs.

// setupNodeChoice is one machine as the picker should see it.
type setupNodeChoice struct {
	DaemonID string
	Machine  string
	Online   bool

	// Handle is a checkout the machine ALREADY has — binding it is instant.
	Handle string
	// DestinationHandle is a directory it offers to clone into — minutes, not milliseconds.
	DestinationHandle string
	Parent            string
}

// Instant reports whether binding this node is immediate. The difference between binding a checkout
// a box already has and cloning a large repo onto it is a thousandfold, and a picker that hides
// which is which will have people wondering whether it hung.
func (n setupNodeChoice) Instant() bool { return n.Handle != "" }

// setupCandidates works out, for one project label and repo remote, what every machine could do.
//
// A machine appears ONCE, with the best offer it has: a matching checkout beats a destination to
// clone into, because instant beats minutes and because a box that already has the repo is the one
// the operator most likely meant.
func setupCandidates(machines []api.MachineOffer, label, remote string) []setupNodeChoice {
	var out []setupNodeChoice
	for _, m := range machines {
		choice := setupNodeChoice{DaemonID: m.DaemonID, Machine: m.Machine, Online: m.Online}

		for _, r := range m.Repos {
			// The handle namespace is opaque and machine-minted; the NAME is what a person recognises,
			// and a repo whose name matches the label is the checkout they mean.
			if strings.EqualFold(r.Name, label) {
				choice.Handle, choice.Parent = r.Handle, r.Parent
				break
			}
		}
		if choice.Handle == "" && len(m.Destinations) > 0 {
			// No local copy: offer the first place it says it can clone into. The repo URL rides
			// separately (the project carries it), so this stays a handle, never a path.
			choice.DestinationHandle = m.Destinations[0].Handle
			choice.Parent = m.Destinations[0].Parent
		}
		if choice.Handle == "" && choice.DestinationHandle == "" {
			continue // nothing this machine can offer for this project
		}
		out = append(out, choice)
	}

	// Online and instant first: the machines that can be working in a second, before the ones that
	// need a clone or are not even up.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Online != out[j].Online {
			return out[i].Online
		}
		if out[i].Instant() != out[j].Instant() {
			return out[i].Instant()
		}
		return strings.ToLower(out[i].Machine) < strings.ToLower(out[j].Machine)
	})
	return out
}

// renderSetupChoices is the question turn 1 hands back.
func renderSetupChoices(label string, nodes []setupNodeChoice) string {
	var s strings.Builder
	fmt.Fprintf(&s, "\nWHICH MACHINES SHOULD BE ABLE TO BUILD %q?\n\n", label)
	if len(nodes) == 0 {
		s.WriteString("  None of this account's machines can offer a directory for it.\n" +
			"  On the box you want work to run on, either check the repo out and run\n" +
			"      ptln daemon add-project " + label + " <dir>\n" +
			"  or give it somewhere to clone into with `ptln daemon scan-root add <dir>`.\n")
		return s.String()
	}
	for _, n := range nodes {
		how := "would CLONE it into " + n.Parent + " (takes a few minutes)"
		if n.Instant() {
			how = "already has this checkout — ready immediately"
		}
		state := ""
		if !n.Online {
			state = "  [offline — it will pick this up when it reconnects]"
		}
		fmt.Fprintf(&s, "  · %s — %s%s\n", n.Machine, how, state)
	}
	s.WriteString("\nASK THE OPERATOR which of these to enable, by name, and then call setup_project\n" +
		"again with `machines` set to their choice. Do NOT choose for them and do not default to all.\n\n" +
		"Say this when you ask, because it is what they are agreeing to: enabling a machine is a\n" +
		"GRANT — it declares that directory available for the team's agents to build in unattended.\n")
	return s.String()
}

// setupBindResult is what happened to one machine.
type setupBindResult struct {
	Machine string
	State   string // ready | cloning | queued | failed | offline
	Reason  string
}

// bindSetupNodes points each chosen machine at the project and waits for the ones that can land
// quickly, so the operator hears an outcome rather than a promise.
//
// Cloning is NOT waited out. A large repo can take minutes and holding a tool call open that long
// is how a session appears to hang; a clone is reported as in-flight with the command to check it.
func bindSetupNodes(c *api.Client, label string, chosen []setupNodeChoice) []setupBindResult {
	var out []setupBindResult
	for _, n := range chosen {
		if err := c.BindMachineProject(n.DaemonID, n.Handle, n.DestinationHandle, label, "", "", ""); err != nil {
			out = append(out, setupBindResult{Machine: n.Machine, State: "failed", Reason: err.Error()})
			continue
		}
		switch {
		case !n.Online:
			out = append(out, setupBindResult{Machine: n.Machine, State: "offline",
				Reason: "queued — it registers when the machine reconnects"})
		case n.Instant():
			state, reason := waitForBind(c, label, n.Machine)
			out = append(out, setupBindResult{Machine: n.Machine, State: state, Reason: reason})
		default:
			out = append(out, setupBindResult{Machine: n.Machine, State: "cloning",
				Reason: "a clone is running on that machine; `ptln project show " + label + "` reports when it lands"})
		}
	}
	return out
}

// waitForBind polls the assignment wire briefly for an instant bind to land.
//
// Bounded hard: binding a checkout the machine already has is a registry write and a re-advertise,
// so it completes in seconds or something is wrong — and "still registering" is a better answer
// than a tool call that never returns. Anything slower (a clone) is never waited on at all.
func waitForBind(c *api.Client, label, machine string) (string, string) {
	deadline := time.Now().Add(12 * time.Second)
	for {
		if as, err := c.Assignments(label); err == nil {
			for _, a := range as {
				if !strings.EqualFold(a.Machine, machine) {
					continue
				}
				switch a.State {
				case "ready", "failed":
					return a.State, a.Reason
				}
			}
		}
		if !time.Now().Before(deadline) {
			return "registering", "still working — `ptln project show " + label + "` reports when it lands"
		}
		time.Sleep(1500 * time.Millisecond)
	}
}

// renderSetupOutcome is what turn 2 hands back: what is runnable, what is still working, and the
// next thing worth doing.
func renderSetupOutcome(set *projectSetup, results []setupBindResult) string {
	var s strings.Builder
	fmt.Fprintf(&s, "PROJECT %q IS SET UP.\n\n", set.Label)
	fmt.Fprintf(&s, "  thread   %s (pinned in .partyline.json — check that file in)\n", set.Thread)
	fmt.Fprintf(&s, "  here     %s\n", set.Path)

	if len(results) > 0 {
		s.WriteString("\nMACHINES\n")
		ready := 0
		for _, r := range results {
			line := "  · " + r.Machine + " — " + r.State
			if r.Reason != "" {
				line += ": " + r.Reason
			}
			s.WriteString(line + "\n")
			if r.State == "ready" {
				ready++
			}
		}
		if ready > 0 {
			fmt.Fprintf(&s, "\n%d machine(s) can build this now. Work promoted to %q will dispatch to them.\n", ready, set.Label)
		}
	}

	s.WriteString("\nTELL THE OPERATOR, in your own words:\n" +
		"  1. what was set up, and that enabling those machines is a GRANT — their agents may now\n" +
		"     build in those directories unattended. Say it plainly; it is not a detail.\n" +
		"  2. that anything still cloning will finish on its own.\n\n" +
		"THEN, WITHOUT ANOTHER REQUEST FROM THEM: interview them about this project and record it.\n" +
		"Ask what it is, who it is for, how it is built and run, and what 'done' looks like here —\n" +
		"a handful of questions, not a form. Write the answers with planning_open/planning_note so\n" +
		"the fleet inherits the same understanding you just built. If they would rather skip it, that\n" +
		"is fine: the project already works, and this only makes what runs on it better.\n")
	return s.String()
}

// chooseSetupNodes matches the operator's answer to the candidate list.
//
// Matched on the machine NAME because that is what the operator was shown and what they will type.
// Anything unmatched is reported rather than silently dropped: quietly enrolling nothing, or the
// wrong box, is worse than saying which name did not resolve.
func chooseSetupNodes(nodes []setupNodeChoice, want []string) (chosen []setupNodeChoice, unknown []string) {
	for _, w := range want {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		hit := false
		for _, n := range nodes {
			if strings.EqualFold(n.Machine, w) || strings.EqualFold(shortHost(n.Machine), shortHost(w)) {
				chosen = append(chosen, n)
				hit = true
				break
			}
		}
		if !hit {
			unknown = append(unknown, w)
		}
	}
	return chosen, unknown
}

// handleSetupProject is the MCP entry point for the whole arc.
//
// Turn 1 sets the repo up and hands back the machine list as a question. Turn 2 takes the answer,
// binds each machine and reports. The two turns share this one tool name so a model cannot get
// halfway and stop: the first response tells it exactly what the second call needs.
func (s *cgServer) handleSetupProject(enc *json.Encoder, req rpcReq) {
	var pp struct {
		Args struct {
			Label    string   `json:"label"`
			Machines []string `json:"machines"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &pp)

	// Setting the repo up is idempotent — it adopts an existing project and thread rather than
	// making a second one — so turn 2 runs it again harmlessly and works even if turn 1 never did.
	set, msg, isErr := createProjectHere(s.c, pp.Args.Label)
	if isErr || set == nil {
		s.toolResult(enc, req.ID, msg, true)
		return
	}
	// The in-process rebind, as create_project does: planning works in THIS session, no relaunch.
	s.thread = set.Thread
	s.repoLookupDone = true
	s.markConnected()

	machines, merr := s.c.MachineOffers()
	if merr != nil {
		s.toolResult(enc, req.ID, msg+"\n\nThe project is set up, but this machine's fleet could not be "+
			"listed, so no other machines were enabled: "+merr.Error()+
			"\n\nTell the operator the project works here, and that other machines can be added later "+
			"with setup_project once the fleet is reachable.", false)
		return
	}
	nodes := setupCandidates(machines, set.Label, "")

	// TURN 1 — nothing chosen yet, so ask.
	if len(pp.Args.Machines) == 0 {
		s.toolResult(enc, req.ID, msg+"\n"+renderSetupChoices(set.Label, nodes), false)
		return
	}

	// TURN 2 — bind what they picked.
	chosen, unknown := chooseSetupNodes(nodes, pp.Args.Machines)
	out := renderSetupOutcome(set, bindSetupNodes(s.c, set.Label, chosen))
	if len(unknown) > 0 {
		out += "\nNOT RECOGNISED: " + strings.Join(unknown, ", ") +
			" — no machine by that name offers a directory for this project. Say so rather than " +
			"assuming it worked, and re-run setup_project if they meant a different one.\n"
	}
	s.toolResult(enc, req.ID, out, false)
}
