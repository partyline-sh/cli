package main

import (
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// cg_fleet_tools.go — rendering for the tools that let an agent OPERATE partyline, not just plan in
// it: read the board, see what machines can be pointed at, and set one up.
//
// These are prose, not JSON, on purpose. The consumer is a model that has to tell a human what is
// happening, and a table it can read aloud produces a better answer than a structure it has to
// summarise. Every one of them names the NEXT action, because a tool result that reports state
// without a next step is how a session stalls.

// renderBoard prints the board the way someone would describe it: what is moving, what is stuck,
// what needs a human. Counts first — the shape of the board is usually the whole answer.
func renderBoard(b *api.Board) string {
	var s strings.Builder
	fmt.Fprintf(&s, "WORK BOARD — backlog %d · building %d · blocked %d · review %d · accepted %d\n",
		len(b.Backlog), len(b.Building), len(b.Blocked), len(b.Review), len(b.Accepted))

	col := func(title string, cards []api.BoardCard, note string) {
		if len(cards) == 0 {
			return
		}
		fmt.Fprintf(&s, "\n%s (%d)%s\n", title, len(cards), note)
		for i, c := range cards {
			if i == 12 {
				fmt.Fprintf(&s, "  … and %d more\n", len(cards)-12)
				break
			}
			line := "  · " + clipLine(c.Task, 78)
			if c.Machine != "" {
				line += "  [" + c.Machine + "]"
			}
			if c.PRURL != "" {
				line += "  " + c.PRURL
			}
			s.WriteString(line + "\n")
		}
	}
	col("BACKLOG", b.Backlog, " — planned, not started")
	col("BUILDING", b.Building, " — running now")
	col("BLOCKED", b.Blocked, " — needs a human: failed, or waiting for approval")
	col("REVIEW", b.Review, " — finished, waiting to be accepted")
	col("ACCEPTED", b.Accepted, " — signed off by a human")

	if len(b.Blocked) > 0 {
		s.WriteString("\nThe BLOCKED column is the one to act on — that work has stopped and will not move on its own.\n")
	}
	return s.String()
}

// renderMachines lists what each machine can be pointed at. Handles are shown because they are the
// only thing add_machine_project accepts — a name would be ambiguous, and a path is never sent.
func renderMachines(ms []api.MachineOffer) string {
	if len(ms) == 0 {
		return "You have no machines enrolled. Run `ptln setup` on a machine to enrol it — it has to be done there, " +
			"because enrolling is what grants your team permission to run code on that box."
	}
	var s strings.Builder
	fmt.Fprintf(&s, "YOUR MACHINES (%d)\n", len(ms))
	for _, m := range ms {
		state := "offline"
		if m.Online {
			state = "online"
		}
		fmt.Fprintf(&s, "\n%s — %s\n", m.Machine, state)
		if len(m.Repos) > 0 {
			s.WriteString("  repositories it has (bind one with repo_handle):\n")
			for i, r := range m.Repos {
				if i == 15 {
					fmt.Fprintf(&s, "    … and %d more\n", len(m.Repos)-15)
					break
				}
				where := r.Parent
				if where != "" {
					where = "  in " + where
				}
				fmt.Fprintf(&s, "    %s  %s%s\n", r.Handle, r.Name, where)
			}
		}
		if len(m.Destinations) > 0 {
			s.WriteString("  directories it can clone INTO (use destination_handle):\n")
			for _, d := range m.Destinations {
				label := d.Label
				if label == "" {
					label = d.Parent
				}
				fmt.Fprintf(&s, "    %s  %s\n", d.Handle, label)
			}
		}
		if len(m.Repos) == 0 && len(m.Destinations) == 0 {
			s.WriteString("  (advertises nothing yet — an older daemon, or it has not sent a heartbeat since enrolling)\n")
		}
	}
	s.WriteString("\nSet one up with add_machine_project(machine, project_label, repo_handle | destination_handle).\n")
	return s.String()
}

// machineByName resolves a machine by the name a human used. Returns an ACTIONABLE reason when it
// cannot: "not found" alone costs the model a turn and the user a round trip.
func machineByName(ms []api.MachineOffer, want string) (*api.MachineOffer, string) {
	want = strings.TrimSpace(want)
	var partial []int
	for i := range ms {
		if strings.EqualFold(ms[i].Machine, want) {
			return &ms[i], ""
		}
		if strings.Contains(strings.ToLower(ms[i].Machine), strings.ToLower(want)) {
			partial = append(partial, i)
		}
	}
	if len(partial) == 1 {
		return &ms[partial[0]], ""
	}
	names := make([]string, 0, len(ms))
	for _, m := range ms {
		names = append(names, m.Machine)
	}
	if len(partial) > 1 {
		return nil, fmt.Sprintf("%q matches more than one machine (%s). Use the full name.", want, strings.Join(names, ", "))
	}
	if len(names) == 0 {
		return nil, "You have no machines enrolled. Run `ptln setup` on the machine you want to use — enrolling has " +
			"to happen there, because it grants your team permission to run code on that box."
	}
	return nil, fmt.Sprintf("No machine called %q. You have: %s.", want, strings.Join(names, ", "))
}

// existingHandleFor finds the handle a machine already advertises whose repository name matches the
// label — the directory a run-mode change must keep pointing at. Changing the mode must never move
// the project to a different directory as a side effect.
func existingHandleFor(m *api.MachineOffer, label string) string {
	for _, r := range m.Repos {
		if strings.EqualFold(r.Name, label) {
			return r.Handle
		}
	}
	return ""
}

// renderPickHandle is the refusal when no handle was given: it lists what this machine actually
// offers, so the next call can succeed instead of guessing.
func renderPickHandle(m *api.MachineOffer, label string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Pick which directory on %s should be %q — pass repo_handle or destination_handle.\n", m.Machine, label)
	if h := existingHandleFor(m, label); h != "" {
		fmt.Fprintf(&s, "\nLikely: repo_handle %s (a repository called %q is already there).\n", h, label)
	}
	if len(m.Repos) > 0 {
		s.WriteString("\nrepositories it has:\n")
		for i, r := range m.Repos {
			if i == 15 {
				break
			}
			fmt.Fprintf(&s, "  %s  %s\n", r.Handle, r.Name)
		}
	}
	if len(m.Destinations) > 0 {
		s.WriteString("\ndirectories it can clone into:\n")
		for _, d := range m.Destinations {
			fmt.Fprintf(&s, "  %s  %s\n", d.Handle, firstNonEmpty(d.Label, d.Parent))
		}
	}
	return s.String()
}

// renderBindResult states what was granted, in the words that matter to the person who owns the
// machine. Registration lets a team's agents run code there unattended, so it is never reported as a
// bare success.
func renderBindResult(tool string, m *api.MachineOffer, label string, cloning bool, policy string) string {
	var s strings.Builder
	if tool == "set_run_mode" {
		mode := "runs dispatched work UNATTENDED"
		if policy == "ask" {
			mode = "WAITS for you to approve each run at the daemon console"
		}
		fmt.Fprintf(&s, "%s: %q is now set to %q — it %s.\n", m.Machine, label, policy, mode)
		if policy == "auto" {
			s.WriteString("\nSo promoting work to this project starts it immediately. Tell the user that plainly.\n")
		} else {
			s.WriteString("\nSo promoting work here will QUEUE it until it is approved on that machine.\n")
		}
		return s.String()
	}

	if cloning {
		fmt.Fprintf(&s, "Asked %s to clone %q and register it. It reports progress as it goes — list_machines shows the state.\n", m.Machine, label)
	} else {
		fmt.Fprintf(&s, "%s is now set up for %q. Work can be dispatched there.\n", m.Machine, label)
	}
	if !m.Online {
		fmt.Fprintf(&s, "\nNOTE: %s is currently OFFLINE. It will pick this up the next time it connects.\n", m.Machine)
	}
	s.WriteString("\nTELL THE USER WHAT THIS GRANTED: that directory is now available to their team's agents, which may " +
		"build in it unattended. Undo it with `ptln daemon remove-project " + label + "` on that machine.\n")
	switch policy {
	case "auto":
		s.WriteString("Run mode is \"auto\": promoted work starts there without asking.\n")
	case "ask":
		s.WriteString("Run mode is \"ask\": promoted work waits for approval on that machine.\n")
	default:
		s.WriteString("Run mode was left as it was. set_run_mode changes it.\n")
	}
	return s.String()
}

// clipLine flattens and clips text for a list row, so one long task cannot destroy the shape of the
// board it appears in. Named apart from party_agent.go's oneLine, which takes no width.
func clipLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
