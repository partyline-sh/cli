// Web-assignable projects (client side). The owner assigns a project to this machine FROM THE WEB
// without the control plane ever sending a path: the daemon advertises CANDIDATE directories —
// sourced ONLY from signals it already has (recorded AI-session cwds + the local registry), NO
// filesystem scan — each as a dir basename + an OPAQUE handle. An assignment names a handle + a
// label; the daemon re-derives its candidates, matches the handle back to a LOCAL absolute path,
// and binds it into the registry exactly like `add-project`. The server can only ever bind a
// directory the daemon itself offered → the reference-not-command invariant is untouched.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// maxCandidates bounds the advertised list (and the heartbeat payload). Session cwds are
// newest-first, so the cap keeps the most-relevant dirs.
const maxCandidates = 30

// candidateHandle is the stable, OPAQUE id the server sees for a local absolute path (never the
// path itself). SHA-256 truncated — collisions are irrelevant here since it only indexes this one
// machine's own directories, and it's deterministic so a handle from an advertised candidate
// re-derives identically when an assignment comes back.
func candidateHandle(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:16]
}

type localCandidate struct {
	abs    string
	source string // "session" | "registry"
	label  string // current registry label if this dir is already a project, else ""
}

// gatherCandidates enumerates assignable directories from signals the daemon already has — NO
// filesystem scan: (1) the working dirs of recorded AI CLI sessions (newest-first), (2) dirs
// already in the registry. Deduped by absolute path; only existing directories survive.
func gatherCandidates() []localCandidate {
	reg := loadDaemonRegistry()
	registered := map[string]string{}
	for _, p := range reg.Projects {
		if p.Path != "" {
			registered[p.Path] = p.Label
		}
	}
	seen := map[string]bool{}
	var out []localCandidate
	push := func(raw, source string) {
		if strings.TrimSpace(raw) == "" {
			return
		}
		abs, err := filepath.Abs(raw)
		if err != nil || seen[abs] {
			return
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return // stale/moved session cwd, or a file — skip
		}
		seen[abs] = true
		out = append(out, localCandidate{abs: abs, source: source, label: registered[abs]})
	}
	for _, s := range collectSessions() { // (1) session cwds — collectSessions already sorts newest-first
		push(s.Cwd, "session")
	}
	for _, p := range reg.Projects { // (2) already-registered dirs (surfaced so the web shows the full picture)
		push(p.Path, "registry")
	}
	return out
}

// candidateSnapshot is the metadata-only candidate list carried on the heartbeat: basename +
// opaque handle + source + (if already a project) its label. Capped, and — like the rest of the
// snapshot — it emits NO absolute path, by construction.
func candidateSnapshot() []api.DaemonCandidate {
	cands := gatherCandidates()
	if len(cands) > maxCandidates {
		cands = cands[:maxCandidates]
	}
	out := make([]api.DaemonCandidate, 0, len(cands))
	for _, c := range cands {
		base := filepath.Base(c.abs)
		out = append(out, api.DaemonCandidate{
			Handle:    candidateHandle(c.abs),
			DirBase:   base,
			Suggested: suggestLabel(base),
			Source:    c.source,
			Label:     c.label,
		})
	}
	return out
}

// resolveCandidateHandle maps a web-supplied handle back to a LOCAL absolute path by re-deriving
// the daemon's current candidates and matching. Returns "" if no current candidate matches — so
// the web can never bind a path the daemon isn't presently advertising.
func resolveCandidateHandle(handle string) string {
	return matchHandle(gatherCandidates(), handle)
}

// matchHandle is the pure match: the local abs path whose handle equals the web-supplied one, or
// "" if none (including empty handle). Split out so the reject-unknown-handle invariant is testable
// without touching the filesystem.
func matchHandle(cands []localCandidate, handle string) string {
	if handle == "" {
		return ""
	}
	for _, c := range cands {
		if candidateHandle(c.abs) == handle {
			return c.abs
		}
	}
	return ""
}

// suggestLabel derives a valid default label from a dir basename (labelRe charset: alnum/space/
// ._-, must start alnum, ≤48). Illegal runes become '-'. Falls back to "project" if nothing valid
// remains. Rune-based so a multibyte name never yields invalid UTF-8.
func suggestLabel(base string) string {
	var b strings.Builder
	for _, r := range base {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'),
			r == '_' || r == '.' || r == '-' || r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.TrimLeft(strings.TrimSpace(b.String()), " _.-") // labelRe requires a leading alnum
	rs := []rune(s)
	if len(rs) > 48 {
		s = string(rs[:48])
	}
	if s == "" {
		return "project"
	}
	return s
}

// bindAssignedProject is the daemon's handler for a web `assign_project` event. It resolves the
// opaque handle to a LOCAL dir the daemon advertised, validates the label/preset/engine, upserts
// the registry, and re-mirrors — the identical binding `add-project` produces, just initiated from
// the web. Rejects any handle that isn't a current candidate (server can't invent a path).
func bindAssignedProject(d daemonDevice, ev api.AssignProjectEvent) error {
	label := strings.TrimSpace(ev.Label)
	if !labelRe.MatchString(label) {
		return fmt.Errorf("invalid label %q", ev.Label)
	}
	abs := resolveCandidateHandle(ev.Handle)
	if abs == "" {
		return fmt.Errorf("handle is not a current candidate on this machine")
	}
	preset := ev.Preset
	if preset != "spec" && preset != "chat" {
		preset = "spec"
	}
	engine := strings.ToLower(strings.TrimSpace(ev.Engine))
	if engine != "" && !validEngine(engine) {
		return fmt.Errorf("invalid engine %q", ev.Engine)
	}
	reg := loadDaemonRegistry()
	upsertProject(&reg, daemonProject{Label: label, Path: abs, Preset: preset, Engine: engine})
	if err := saveDaemonRegistry(reg); err != nil {
		return err
	}
	return mirrorProjects(d)
}

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
