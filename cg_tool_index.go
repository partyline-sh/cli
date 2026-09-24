package main

// One line per MCP tool, for `ptln help`.
//
// The tool DESCRIPTIONS in cgToolDefs are written for an agent deciding whether to call something:
// long, imperative, and often shouting. A person scanning `ptln help` needs the opposite. So the
// human line lives here, and cgToolIndexIsComplete holds this map to the advertised list in both
// directions — a tool added without a line, or a line left behind by a deleted tool, fails the
// build rather than reaching a reader.

import (
	"fmt"
	"io"
)

// cgToolSummary is tool name → one line, shown in `ptln help`.
var cgToolSummary = map[string]string{
	// shared memory
	"project_memory":       "what the team has learned about this project",
	"remember":             "record ONE durable fact: decision | constraint | contract | gotcha | question",
	"propose_fact":         "record a fact mined from another system — citations required",
	"setup_project_memory": "set this repo up as part of a project, when it belongs to none and wants shared memory",
	"setup_project":        "set this repo up as part of a project, from the session",

	// the thread — one effort's working feed
	"recall":           "the thread's shared context (narrows to one entity slug)",
	"read_context":     "the whole thread feed",
	"curate":           "propose a synthesized brief for the thread, naming the facts it stands in for",
	"publish_artifact": "show the user what you are about to build, as a self-contained HTML page",
	"read_marks":       "read what the user drew on a work item's worked example — typed, anchored marks",

	// planning
	"planning_open":     "start a planning interview",
	"planning_note":     "record an answer into the open plan",
	"planning_finalize": "file the plan as ONE backlog card — refuses while anything is unanswered",
	"promote_work_item": "start a filed item on a machine",
	"send_to_partyline": "send work to the backlog, or hand back the questions to ask first",

	// reading the fleet
	"read_board":          "every card, in the column it is in: backlog, building, blocked, review, shipped",
	"read_fleet":          "the machines, and what each is doing",
	"list_machines":       "the user's machines and what each can be pointed at: repos it has, dirs it offers",
	"add_machine_project": "point a machine at one of the directories it offers, making it a node for a project",
	"set_run_mode":        "whether dispatched work starts unattended (auto) or waits for approval (ask)",

	// diagnosis
	"read_run":     "one run's state, its tasks, branches and PRs. Read-only",
	"read_run_log": "the tail of a run's output — redacted, fenced, labelled UNTRUSTED",

	// asking someone else
	"list_sessions": "other live sessions on THIS machine",
	"ask_session":   "ask one of them, and get the answer from its warm context",
	"list_peers":    "teammates' machines you may ask, and about which projects",
	"ask_peer":      "ask a teammate's agent a read-only question",
	"check_consult": "collect a peer answer that landed later",

	// instance admin
	"setup_read":  "what this instance still needs. Instance-admin only",
	"setup_write": "name the deployment, open or close signups. Instance-admin only",
}

// cgToolIndexIsComplete reports the tools advertised with no summary, and the summaries naming a
// tool that is no longer advertised. Both are failures; see TestCgToolIndexIsComplete.
func cgToolIndexIsComplete() (missing, stale []string) {
	advertised := map[string]bool{}
	for _, d := range cgToolDefs {
		name, _ := d["name"].(string)
		advertised[name] = true
		if cgToolSummary[name] == "" {
			missing = append(missing, name)
		}
	}
	for name := range cgToolSummary {
		if !advertised[name] {
			stale = append(stale, name)
		}
	}
	return missing, stale
}

// writeCgToolIndex renders the MCP tool list for `ptln help`, in the order they are advertised.
func writeCgToolIndex(w io.Writer, indent string) {
	for _, d := range cgToolDefs {
		name, _ := d["name"].(string)
		fmt.Fprintf(w, "%s%-21s %s\n", indent, name, cgToolSummary[name])
	}
}
