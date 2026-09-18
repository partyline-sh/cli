//go:build darwin && tray

package main

import "fmt"

// projects.go — the team's projects, in the menu, and a banner when a new one appears.
//
// The list is the daemon's canonical-projects cache riding `ptln state` (autoadopt.go) — names and
// flags only, never a path, and never fetched from here: the tray stays network-free. Each row says
// the one thing worth knowing at a glance: whether THIS machine can build it.
//
// EDGE-TRIGGERED notification, same contract as finished_runs.go: announce a project ID we have NOT
// seen, never a list that merely persists, and seed silently on the first poll so starting the tray
// doesn't re-announce the whole team's portfolio.

// maxProjects bounds the menu section. systray can't grow a menu after start, so rows are
// pre-allocated; a team with more projects than this sees the overflow line.
const maxProjects = 8

// trayProject mirrors the CLI's canonicalProject — only what a menu row and a banner need.
type trayProject struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"`
	Mine        bool   `json:"mine"`
	HasRepo     bool   `json:"has_repo"`
	AdoptedHere bool   `json:"adopted_here"`
}

// projectLine is one menu row: buildable-here mark, name, and a private tag when it applies.
func projectLine(p trayProject) string {
	mark := "·" // visible, not buildable on this machine
	if p.AdoptedHere {
		mark = "✓" // registered here — work can land on this box
	}
	name := p.DisplayName
	if name == "" {
		name = p.Label
	}
	line := mark + " " + name
	if p.Visibility == "private" {
		line += " — private"
	}
	return line
}

// projectWatch notifies once per newly-seen project.
type projectWatch struct {
	seen   map[string]bool
	seeded bool
}

func newProjectWatch() *projectWatch { return &projectWatch{seen: map[string]bool{}} }

// notices returns the banners to post for this poll, and records what it saw. `ok` guards the seen
// set the same way the peer section guards its edges: a failed read must not blank the memory and
// re-announce everything when the CLI comes back.
func (w *projectWatch) notices(ok bool, rows []trayProject) []string {
	if !ok {
		return nil
	}
	var out []string
	for _, p := range rows {
		if p.ID == "" || w.seen[p.ID] {
			continue
		}
		w.seen[p.ID] = true
		if w.seeded {
			out = append(out, projectBody(p))
		}
	}
	w.seeded = true
	return out
}

// projectBody names the project and what it means for THIS machine — "a project exists" is not
// worth a banner unless it says whether you can already build on it here.
func projectBody(p trayProject) string {
	name := p.DisplayName
	if name == "" {
		name = p.Label
	}
	if p.AdoptedHere {
		return fmt.Sprintf("New project: %s — ready on this machine", name)
	}
	return fmt.Sprintf("New project: %s", name)
}
