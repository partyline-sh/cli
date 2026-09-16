// Trust · G.1 — prove a judging lane left the code alone.
//
// The verify gate's reviewer runs with no tools, and the visual lane runs a repo-authored script.
// Both are supposed to READ the worktree and never write to it. Until now that was an assertion in
// a comment: nothing checked, so a reviewer that edited the diff it was judging would have been
// invisible, and its verdict would have been about code it had itself changed.
//
// WHAT IS CHECKED, AND WHY THREE THINGS RATHER THAN ONE.
//
// The obvious implementation — and the one a comparable system ships — hashes `git status` before
// and after. That misses the case that matters most: a lane that COMMITS its edits leaves a clean
// status, so the tree looks untouched and the mutation is invisible. Recording HEAD closes that.
// The stash count closes the variant where work is parked rather than committed.
//
// None of this defends against a determined attacker with shell access on the machine — a lane
// that can run `git` can also restore what it changed. It is not meant to. It defends against the
// failure that actually happens: a model given file-editing tools by accident, or a repo-authored
// render script with a side effect nobody intended. Cheap, and it turns "the reviewer is read-only"
// from a claim into a recorded observation.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/gate"
	"partyline.sh/partyline/internal/surface"
)

// worktreeState is a bounded fingerprint of everything a lane could plausibly disturb.
type worktreeState struct {
	statusHash string   // sha256 of `git status --porcelain=v1 -z`
	head       string   // the commit HEAD points at
	stashes    int      // entries in the stash list
	files      []string // paths git reports as dirty, for the evidence line
	observed   bool     // false when git could not be read — then we make NO claim either way
}

// snapshotWorktree records the state a lane must leave unchanged. A failure to read git is not a
// failure of the lane: it yields observed=false, and an unobserved lane is reported as making no
// read-only claim rather than as passing one.
func snapshotWorktree(wtPath string) worktreeState {
	st := worktreeState{}
	raw, err := gitOut(wtPath, "status", "--porcelain=v1", "-z")
	if err != nil {
		return st
	}
	sum := sha256.Sum256([]byte(raw))
	st.statusHash = hex.EncodeToString(sum[:])
	st.files = porcelainPaths(raw)

	head, err := gitOut(wtPath, "rev-parse", "HEAD")
	if err != nil {
		return st
	}
	st.head = strings.TrimSpace(head)

	// A repo with no stashes prints nothing and exits 0, so an error here is a real failure to read.
	stash, err := gitOut(wtPath, "stash", "list")
	if err != nil {
		return st
	}
	st.stashes = len(nonEmptyLines(stash))
	st.observed = true
	return st
}

// compareWorktree turns two snapshots into the proof recorded on the report.
func compareWorktree(before, after worktreeState) gate.ReadOnlyProof {
	p := gate.ReadOnlyProof{
		Observed:     before.observed && after.observed,
		StatusBefore: before.statusHash,
		StatusAfter:  after.statusHash,
		HeadBefore:   before.head,
		HeadAfter:    after.head,
		StashBefore:  before.stashes,
		StashAfter:   after.stashes,
	}
	if !p.Observed {
		// We could not look. Passed stays false — this is an absence of evidence, and reporting it
		// as a pass would be the same lie as calling a skipped gate a pass.
		return p
	}
	p.Passed = before.statusHash == after.statusHash &&
		before.head == after.head &&
		before.stashes == after.stashes
	if !p.Passed {
		p.Changed = changedPaths(before, after)
	}
	return p
}

// readOnlyLane renders the proof as a report lane. A mutation is BLOCKING: a verdict about code the
// judge itself edited is not a verdict, so it cannot be allowed to pass.
func readOnlyLane(name string, p gate.ReadOnlyProof) gate.CheckResult {
	c := gate.CheckResult{Name: name, Kind: gate.KindReadOnly, Blocking: true}
	switch {
	case !p.Observed:
		// Honest silence. The lane still ran; we simply have no claim to make about it.
		c.Status, c.Code = gate.StatusSkip, surface.CodeSkipped
		c.Detail = "could not read the worktree state — no read-only claim made"
	case p.Passed:
		c.Status, c.Code = gate.StatusPass, surface.CodeOK
	default:
		c.Status, c.Code = gate.StatusFail, surface.CodeReadOnlyMutated
		c.Detail = mutationDetail(p)
		for _, f := range p.Changed {
			c.Evidence = append(c.Evidence, gate.Evidence{Kind: "file", Path: f, Note: "changed while being judged"})
		}
	}
	return c
}

// mutationDetail names WHICH signal moved, because "the reviewer modified something" is not
// actionable and "the reviewer committed" is.
func mutationDetail(p gate.ReadOnlyProof) string {
	var why []string
	if p.HeadBefore != p.HeadAfter {
		why = append(why, "it committed (HEAD moved "+shortSHA(p.HeadBefore)+" → "+shortSHA(p.HeadAfter)+")")
	}
	if p.StatusBefore != p.StatusAfter {
		why = append(why, "the working tree changed")
	}
	if p.StashBefore != p.StashAfter {
		why = append(why, "it stashed work")
	}
	d := "a judging lane modified the code it was reviewing: " + strings.Join(why, "; ")
	if len(p.Changed) > 0 {
		d += " — " + strings.Join(p.Changed, ", ")
	}
	return gate.Truncate(d, 1500)
}

// changedPaths is the symmetric difference of the two dirty-file sets: what appeared, and what
// stopped being dirty (which is what a lane that committed or reverted looks like).
func changedPaths(before, after worktreeState) []string {
	was := map[string]bool{}
	for _, f := range before.files {
		was[f] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range after.files {
		seen[f] = true
		if !was[f] {
			out = append(out, f)
		}
	}
	for _, f := range before.files {
		if !seen[f] {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	if len(out) > 20 {
		out = append(out[:20:20], "…")
	}
	return out
}

// porcelainPaths pulls the path out of each NUL-separated porcelain record. Rename records carry
// two paths; both matter, and taking the last field of the record catches the destination.
func porcelainPaths(raw string) []string {
	var out []string
	for _, rec := range strings.Split(raw, "\x00") {
		if len(rec) < 4 {
			continue
		}
		out = append(out, strings.TrimSpace(rec[3:]))
	}
	sort.Strings(out)
	return out
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func gitOut(dir string, args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := c.Output()
	return string(b), err
}
