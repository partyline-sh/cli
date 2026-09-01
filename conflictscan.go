package main

import (
	"encoding/json"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// conflictscan.go — Slice A2: the DETECTION half of conflict-aware review. Right after a task's PR
// opens, the daemon test-merges its branch against every OTHER open PR to the same base and reports
// the REAL conflicts (git merge-tree, not mere same-file overlap) with the task result. The control
// plane (A1, already live) then badges the card, banners the drawer, notifies the owner, and blocks
// Accept until the human resolves or acknowledges — so two PRs that touch the same lines can't merge
// into a silent conflict.
//
// Design decisions (locked with the operator):
//   - ALL open PRs to the base are checked — a human's PR conflicts just as hard as an agent's. Only
//     partyline-owned branches (crank-*) are marked resolvable (one-click rebase); human PRs are
//     info-only.
//   - No candidate cap. File-overlap (computed locally from fetched refs — zero extra API calls)
//     pre-filters which candidates get a merge-tree, so the expensive step runs only on real suspects.
//   - Fail-open on tooling, fail-closed on data: if gh or git can't answer (old git without
//     --write-tree, no network), we report NOTHING (checked=false) — the control plane keeps its
//     prior knowledge rather than being told "no conflicts" by a scan that never ran.

// scanPRConflicts returns the open PRs whose branches REALLY conflict with ours, and whether the
// scan actually ran (false = tooling unavailable/failed; report nothing upstream). `branch` is our
// just-pushed task branch; `base` the PR target. Only textual conflicts count — clean-merging
// same-file edits are not flagged (alarm fatigue kills the gate's credibility).
func scanPRConflicts(run cmdRunner, branch, base string) (conflicts []api.PRConflict, checked bool) {
	if branch == "" || strings.HasPrefix(branch, "-") || base == "" || strings.HasPrefix(base, "-") {
		return nil, false
	}
	// Other open PRs to the same base. gh runs with this run's minted token (realRunner) — listing
	// is read-scope the token already holds.
	out, err := run("gh", "pr", "list", "--state", "open", "--base", base, "--json", "number,headRefName")
	if err != nil {
		return nil, false
	}
	var prs []struct {
		Number int    `json:"number"`
		Head   string `json:"headRefName"`
	}
	if json.Unmarshal([]byte(out), &prs) != nil {
		return nil, false
	}

	// Our own change surface vs the base — the overlap pre-filter's left side.
	if _, err := run("git", "fetch", "origin", base); err != nil {
		return nil, false
	}
	ownOut, err := run("git", "diff", "--name-only", "origin/"+base+"..."+branch)
	if err != nil {
		return nil, false
	}
	ownFiles := make(map[string]bool)
	for _, f := range strings.Split(strings.TrimSpace(ownOut), "\n") {
		if f != "" {
			ownFiles[f] = true
		}
	}
	if len(ownFiles) == 0 {
		return []api.PRConflict{}, true // nothing changed → nothing can conflict
	}

	checked = true
	conflicts = []api.PRConflict{}
	for _, pr := range prs {
		head := strings.TrimSpace(pr.Head)
		// Skip ourselves (matched by branch — the surest self-identity we hold) and junk.
		if head == "" || head == branch || strings.HasPrefix(head, "-") {
			continue
		}
		// Fetch the candidate's head once; FETCH_HEAD serves both the overlap check and merge-tree.
		if _, err := run("git", "fetch", "origin", "refs/heads/"+head); err != nil {
			continue // deleted branch / permissions — not our conflict to report
		}
		candOut, err := run("git", "diff", "--name-only", "origin/"+base+"...FETCH_HEAD")
		if err != nil {
			continue
		}
		overlap := false
		for _, f := range strings.Split(strings.TrimSpace(candOut), "\n") {
			if ownFiles[f] {
				overlap = true
				break
			}
		}
		if !overlap {
			continue
		}
		// The real test: does merging the two branches produce textual conflicts? merge-tree writes
		// nothing to the working tree; exit 1 = conflicted, output lists the conflicted paths after
		// the tree OID (--name-only, messages suppressed). An "unknown option" failure means git is
		// too old for --write-tree — the whole scan is unusable, not just this candidate.
		mtOut, mtErr := run("git", "merge-tree", "--write-tree", "--name-only", "--no-messages", branch, "FETCH_HEAD")
		if mtErr == nil {
			continue // clean merge — same files, different regions; not a conflict
		}
		if strings.Contains(mtOut, "unknown option") || strings.Contains(mtOut, "usage: git merge-tree") {
			return nil, false
		}
		var files []string
		for i, line := range strings.Split(strings.TrimSpace(mtOut), "\n") {
			line = strings.TrimSpace(line)
			if i == 0 || line == "" { // first line is the written tree OID
				continue
			}
			files = append(files, line)
		}
		conflicts = append(conflicts, api.PRConflict{
			PR: pr.Number, Branch: head, Files: files,
			// Ours to auto-rebase only when partyline made it — a human's branch is theirs.
			Resolvable: strings.HasPrefix(head, "crank-"),
		})
	}
	return conflicts, checked
}
