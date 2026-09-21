package main

// The second ribbon row: partyline's own state, under the tabs.
//
// Everything this system does is otherwise invisible until it speaks inside a session — which
// is right for the agent and wrong for the human. The operator needs to know, without asking,
// that this window is in a project, that the team has recorded things, that something is not
// synced, and that two facts disagree. A status line is the honest place for that: it costs no
// keystroke and interrupts nothing.
//
// Printed by `ptln tmux --status <dir>`, which tmux runs on its status interval for the active
// pane's directory. It must therefore be FAST and never block: a slow status command stalls the
// whole bar redraw, so nothing here touches the network — sync state is read from git's own
// local refs, not from a fetch.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/brand"
)

// memoryStatusLine renders the row for a directory. Empty string means "say nothing", which is
// what a directory outside any project gets in a bar that is not ours to fill with noise.
func memoryStatusLine(dir string) string {
	ws, ok := projectForDir(dir)
	if !ok {
		return ""
	}
	amber, pill, dim := brand.Hex(brand.AmberRGB), brand.Hex(brand.PillRGB), "#8a8a8a"
	facts, err := readFacts(ws.Dir, false)
	if err != nil {
		return fmt.Sprintf("#[fg=%s] ☎ %s #[fg=%s]memory unreadable", amber, ws.Label, pill)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "#[fg=%s,bold] ☎ %s#[default]", amber, ws.Label)
	if repo := ws.repoNameFor(dir); repo != "" {
		fmt.Fprintf(&b, "#[fg=%s] · %s#[default]", dim, repo)
	}
	fmt.Fprintf(&b, "#[fg=%s]  %d learned#[default]", dim, len(facts))

	if n := countSince(facts, time.Now().Add(-24*time.Hour)); n > 0 {
		fmt.Fprintf(&b, "#[fg=%s]  ● %d today#[default]", amber, n)
	}
	if n := len(conflicts(facts)); n > 0 {
		word := "disagreements"
		if n == 1 {
			word = "disagreement"
		}
		fmt.Fprintf(&b, "#[fg=%s,bold]  ⚠ %d %s#[default]", pill, n, word)
	}
	if n := unsyncedCount(ws.Dir); n > 0 {
		fmt.Fprintf(&b, "#[fg=%s]  ⇡ %d unshared#[default]", pill, n)
	}
	// Who else is on this project. Read from a file the watcher maintains, never from the
	// network — see presence.go. Absent when the bus is down, which is a normal state and so
	// says nothing rather than reporting an outage the operator cannot act on.
	if people := peopleOf(readPresence(ws.Label)); len(people) > 0 {
		word := "others"
		if len(people) == 1 {
			word = "other"
		}
		fmt.Fprintf(&b, "#[fg=%s]  ◉ %d %s here#[default]", dim, len(people), word)
	}
	return b.String()
}

func countSince(facts []fact, t time.Time) int {
	n := 0
	for _, f := range facts {
		if f.At.After(t) {
			n++
		}
	}
	return n
}

// unsyncedCount is how many commits this machine holds that teammates do not have, plus any
// uncommitted change. Read from local refs only — a status line must never wait on a network.
// A memory repo with no upstream reports nothing: a project nobody has shared yet is a normal
// state, not a warning to nag about on every redraw.
func unsyncedCount(dir string) int {
	if !hasUpstream(dir) {
		return 0
	}
	if out, err := gitMem(dir, "status", "--porcelain"); err == nil && strings.TrimSpace(out) != "" {
		return 1
	}
	out, err := gitMem(dir, "rev-list", "--count", "@{u}..HEAD")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}
