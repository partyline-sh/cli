// Local project registry maintenance for the daemon.
//
// This file used to also host WEB-ASSIGNABLE PROJECTS: the daemon advertised candidate directories
// (session cwds + registry dirs) as basename + opaque handle, and the web could bind one to a label
// from the project settings page. That whole path is gone — it was never used once in production
// (zero rows in daemon_project_assignments), and provisioned workers make it obsolete: a worker
// clones the repo on demand rather than needing a pre-bound local directory.
//
// Removing it deletes a trust surface too — nothing web-supplied resolves to a local path any more.
// Projects are bound on the machine with `ptln daemon add-project`, full stop.
package main

import (
	"fmt"
	"strings"
)

// relabelProject renames a project in the LOCAL registry old→new and re-mirrors — the daemon side
// of the web rename cascade. The heartbeat mirror replaces the server's advertised labels wholesale,
// so a machine-advertised label only truly changes here. No-op if the old label isn't registered; if
// the new label already exists it wins (the old entry is dropped) via upsertProject.
func relabelProject(d daemonDevice, oldLabel, newLabel string) error {
	newLabel = strings.TrimSpace(newLabel)
	if !labelRe.MatchString(newLabel) {
		return fmt.Errorf("invalid new label %q", newLabel)
	}
	reg := loadDaemonRegistry()
	var moved *daemonProject
	kept := reg.Projects[:0]
	for _, p := range reg.Projects {
		if p.Label == oldLabel {
			p.Label = newLabel
			pp := p
			moved = &pp
			continue
		}
		kept = append(kept, p)
	}
	if moved == nil {
		return nil // this machine doesn't advertise the old label — nothing to rename
	}
	reg.Projects = kept
	upsertProject(&reg, *moved)
	if err := saveDaemonRegistry(reg); err != nil {
		return err
	}
	return mirrorProjects(d)
}

// upsertProject adds or updates a registry entry by label (in place), so the CLI add-project and
// the web assign path bind identically.
func upsertProject(reg *daemonRegistry, p daemonProject) {
	for i := range reg.Projects {
		if reg.Projects[i].Label == p.Label {
			reg.Projects[i].Path, reg.Projects[i].Preset, reg.Projects[i].Engine = p.Path, p.Preset, p.Engine
			return
		}
	}
	reg.Projects = append(reg.Projects, p)
}
