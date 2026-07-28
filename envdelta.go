package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// Environment delta (epic #683, slice 2) — "what is built but not live yet", answered with plain git.
//
// This runs on the machine because the machine is the only place the answer exists. A git host API
// could answer it for GitHub, and a different one for GitLab, and neither for a team on a private
// remote or no remote at all. `git log A..B` answers it for everyone, and it answers it for branches
// that were never pushed — which is the DEFAULT for partyline's own manual merge policy.
//
// The control plane sends the QUESTION (an ordered chain of branch names, plus the task branches it
// created) and gets back the ANSWER. Nothing it sends is ever executed: every branch name is
// re-validated here against branchDeltaRe before it goes anywhere near a command line, and the git
// invocations are fixed argv with `--` separators. A server that wanted to run something on a
// customer's laptop would have to get past a regex that permits no spaces, no dashes at the front,
// and no shell metacharacters at all.

// The branch names we are willing to hand to git. Deliberately stricter than git's own rules: the
// leading character must be alphanumeric so nothing can be read as a flag, and the character class
// excludes everything a shell or git revision syntax treats as special (no `..`-only tricks matter
// because these are always positional operands after `--`, but defence in depth is cheap here).
var branchDeltaRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-/]{0,99}$`)

const (
	maxDeltaCommits  = 50               // capped: a delta is a summary, not a changelog
	deltaGitTimeout  = 25 * time.Second // a big repo's `git log` is slow, but it is not minutes
	deltaFieldSep    = "\x1f"           // unit separator — cannot appear in a commit subject
	deltaRecordSep   = "\x1e"           // record separator — so multi-line subjects can't fake a row
	envReportEvery   = 5 * time.Minute  // how often a daemon re-measures
	envReportInitial = 20 * time.Second // first measurement shortly after start, not immediately
)

// gitDelta runs a git command in the repo and returns stdout. Fixed argv, no shell, and always
// bounded — this runs unattended on someone's laptop, where a pathological repo must degrade to "no
// delta reported" rather than a goroutine wedged on git forever.
func gitDelta(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deltaGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// envGapsFor computes one project's gaps: for every ADJACENT pair in the chain, what is in the
// earlier branch that has not reached the later one.
//
// Direction matters and is easy to get backwards. The chain is ordered the way work TRAVELS, so
// position 0 is where builds land and the last position is production. `git log to..from` therefore
// reads as "commits on `from` that `to` does not have" — the work that is waiting.
func envGapsFor(dir string, q api.EnvQuestion) []api.EnvGap {
	if len(q.Environments) < 2 {
		return nil
	}
	// Resolve branches ONCE. A chain often names a branch that this clone has never fetched (a
	// teammate's environment, or a fresh clone); that is not an error, it just means we cannot
	// measure that gap and must say nothing rather than report zero.
	var gaps []api.EnvGap
	for i := 0; i+1 < len(q.Environments); i++ {
		from, to := q.Environments[i], q.Environments[i+1]
		if !branchDeltaRe.MatchString(from.Branch) || !branchDeltaRe.MatchString(to.Branch) {
			continue
		}
		fromRef, ok := resolveDeltaRef(dir, from.Branch)
		if !ok {
			continue
		}
		toRef, ok := resolveDeltaRef(dir, to.Branch)
		if !ok {
			continue
		}
		commits, authors, total := commitsBetween(dir, toRef, fromRef)
		gaps = append(gaps, api.EnvGap{
			Position:    i,
			FromName:    from.Name,
			ToName:      to.Name,
			FromBranch:  from.Branch,
			ToBranch:    to.Branch,
			CommitCount: total,
			Authors:     authors,
			Commits:     commits,
			Items:       itemsInGap(dir, toRef, fromRef, q.Branches),
		})
	}
	return gaps
}

// resolveDeltaRef finds a usable ref for an environment branch, preferring the REMOTE tracking ref.
//
// The remote is the right default because an environment is a shared fact: what is on `origin/main`
// is what the team has shipped, while a stale local `main` reflects only when this laptop last
// pulled. Falling back to the local branch keeps this working for a repo with no remote at all.
func resolveDeltaRef(dir, branch string) (string, bool) {
	for _, ref := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		if _, err := gitDelta(dir, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return ref, true
		}
	}
	return "", false
}

// commitsBetween returns the commits reachable from `from` but not `to`, newest first, plus the
// distinct authors ordered by how much of the gap is theirs, plus the TOTAL count.
//
// The total is counted separately from the returned slice because the slice is capped: showing "50
// commits" when there are 300 would understate the gap in exactly the situation where the number
// matters most.
func commitsBetween(dir, to, from string) ([]api.EnvCommit, []string, int) {
	format := strings.Join([]string{"%h", "%s", "%an", "%aI"}, deltaFieldSep) + deltaRecordSep
	out, err := gitDelta(dir, "log", "--no-merges", "--pretty=format:"+format, to+".."+from, "--")
	if err != nil {
		return nil, nil, 0
	}
	byAuthor := map[string]int{}
	var commits []api.EnvCommit
	total := 0
	for _, rec := range strings.Split(out, deltaRecordSep) {
		rec = strings.TrimLeft(rec, "\r\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.Split(rec, deltaFieldSep)
		if len(f) < 4 {
			continue
		}
		total++
		byAuthor[f[2]]++
		if len(commits) < maxDeltaCommits {
			commits = append(commits, api.EnvCommit{Sha: f[0], Subject: f[1], Author: f[2], At: f[3]})
		}
	}
	// Most-commits-first, then alphabetical, so the ordering is stable across reports and the UI
	// does not appear to shuffle when nothing changed.
	authors := make([]string, 0, len(byAuthor))
	for a := range byAuthor {
		authors = append(authors, a)
	}
	sort.Slice(authors, func(i, j int) bool {
		if byAuthor[authors[i]] != byAuthor[authors[j]] {
			return byAuthor[authors[i]] > byAuthor[authors[j]]
		}
		return authors[i] < authors[j]
	})
	return commits, authors, total
}

// itemsInGap picks out which of PARTYLINE's own task branches are sitting in this gap.
//
// This is the part a generic git tool cannot do, and it needs no heuristics: partyline created these
// branches, so their names came from us and each maps back to a run and its work item. A branch is
// "in the gap" when the earlier environment contains it and the later one does not — i.e. it has
// been merged as far as staging, and is waiting on production.
//
// `--is-ancestor` is exact for merge commits and fast-forwards. It is deliberately NOT
// supplemented with patch-id matching: a squash-merge rewrites history so the branch tip is no
// longer an ancestor of anything, and inferring equivalence would sometimes be wrong. A
// squash-merged branch simply drops out of `items` while its commit still shows in `commits` —
// undercounting attribution rather than claiming something false.
func itemsInGap(dir, to, from string, branches []api.EnvBranchRef) []api.EnvItem {
	var items []api.EnvItem
	for _, b := range branches {
		if !branchDeltaRe.MatchString(b.Branch) {
			continue
		}
		ref, ok := resolveDeltaRef(dir, b.Branch)
		if !ok {
			continue
		}
		if !isAncestor(dir, ref, from) || isAncestor(dir, ref, to) {
			continue
		}
		items = append(items, api.EnvItem{Branch: b.Branch, RunID: b.RunID})
	}
	return items
}

// isAncestor reports whether `ref` is fully contained in `in`. `git merge-base --is-ancestor` exits
// 0 for yes and 1 for no, so a non-zero exit is an ANSWER, not a failure — but any other error
// (unknown ref, broken repo) also lands here, and answering "no" is the safe direction: it omits an
// item rather than claiming work has shipped when it has not.
func isAncestor(dir, ref, in string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), deltaGitTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", ref, in).Run() == nil
}

// reportEnvDeltas is one full cycle: ask the control plane what to measure, measure it with git,
// send the answer back.
//
// Entirely best-effort. This is a read-only observation of repos the machine already has, and
// nothing downstream depends on it — a failed cycle means the web says "not measured recently",
// which is the correct thing for it to say.
func reportEnvDeltas(d daemonDevice) {
	qs, err := api.EnvQuestions(d.Base, d.Token)
	if err != nil || len(qs) == 0 {
		return
	}
	reg := loadDaemonRegistry()
	dirs := map[string]string{}
	for _, p := range reg.Projects {
		dirs[p.Label] = p.Path
	}

	var reports []api.EnvReport
	for _, q := range qs {
		dir := dirs[q.Label]
		if dir == "" {
			continue // the server named a label this machine no longer has registered
		}
		gaps := envGapsFor(dir, q)
		if gaps == nil {
			gaps = []api.EnvGap{}
		}
		reports = append(reports, api.EnvReport{Label: q.Label, Gaps: gaps})
	}
	if len(reports) == 0 {
		return
	}
	_ = api.ReportEnvDeltas(d.Base, d.Token, reports)
}

// envDeltaSummary renders a one-line human summary of a gap, for `ptln daemon projects` and logs.
func envDeltaSummary(g api.EnvGap) string {
	if g.CommitCount == 0 {
		return fmt.Sprintf("%s → %s: in sync", g.FromName, g.ToName)
	}
	unit := "commits"
	if g.CommitCount == 1 {
		unit = "commit"
	}
	return fmt.Sprintf("%s → %s: %d %s waiting", g.FromName, g.ToName, g.CommitCount, unit)
}
