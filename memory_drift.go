package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Memory goes stale as the code moves, and nothing in the store notices on its own: a fact is
// briefed as correct until someone edits it. Two checks keep it honest, both mechanical:
//
//   - drift: how many code commits have landed since the overview was last verified. Past
//     memoryDriftThreshold, a session tells the user it is time to update the memory.
//   - changed evidence: a fact that names a file is flagged when that file changed after the fact
//     was last verified. It may still be right; someone has to look.
//
// A fact counts as verified at the last commit that changed its file. Stamping HEAD when the fact
// is written was the first design and it was wrong: a session records facts before it commits the
// code they describe, so the stamp predated the code and every such fact was flagged the moment
// the work was committed. A fact whose file is uncommitted is current by definition. Confirming a
// fact with `ptln memory verify` writes `verified:` into it, which changes the file, so committing
// that moves its commit forward.

// memoryDriftThreshold is the number of code commits after which a session suggests updating.
const memoryDriftThreshold = 20

// memoryMetaPaths are excluded when counting code commits: updating memory must not count as
// the code moving on.
var memoryMetaPaths = []string{":(exclude)memory", ":(exclude)AGENTS.md", ":(exclude)CLAUDE.md", ":(exclude)GEMINI.md"}

// headCommit is HEAD of a repo that holds its own memory, or "" (no commits, or a legacy side
// repo whose HEAD says nothing about the code).
func headCommit(dir string) string {
	if !isRepoLocalMemory(dir) {
		return ""
	}
	out, err := gitMem(dir, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return ""
	}
	return out
}

// gitDeadline runs git with a time limit. The hook runs these on a session's first prompt, and a
// slow repo must cost a missed notice, not a stalled prompt.
func gitDeadline(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimRight(string(out), "\n"), err // not TrimSpace: porcelain status lines start with a space
}

// verifiedAt is the last commit that changed f's file, or "" when the file has uncommitted changes
// or was never committed (it was just written or edited, so it is current).
func verifiedAt(root string, f fact) string {
	rel, err := filepath.Rel(root, f.Path)
	if err != nil || f.Path == "" {
		return ""
	}
	if dirty, err := gitMem(root, "status", "--porcelain", "--untracked-files=all", "--", rel); err != nil || dirty != "" {
		return ""
	}
	out, _ := gitMem(root, "log", "-1", "--format=%H", "--", rel)
	return out
}

// factBases is verifiedAt for every fact file at once: one git log over memory/ and one status,
// instead of two git processes per fact.
func factBases(ctx context.Context, root string) map[string]string {
	bases := map[string]string{}
	out, err := gitDeadline(ctx, root, "log", "--format=%x00%H", "--name-only", "--", "memory")
	if err != nil {
		return bases
	}
	for _, rec := range strings.Split(out, "\x00") {
		lines := strings.Split(strings.TrimSpace(rec), "\n")
		if len(lines) < 2 {
			continue
		}
		for _, p := range lines[1:] {
			if p = strings.TrimSpace(p); p != "" && bases[p] == "" {
				bases[p] = lines[0] // log is newest first, so the first commit seen is the last change
			}
		}
	}
	if st, err := gitDeadline(ctx, root, "status", "--porcelain", "--untracked-files=all", "--", "memory"); err == nil {
		for _, l := range strings.Split(st, "\n") {
			if len(l) > 3 {
				delete(bases, strings.Trim(l[3:], `"`))
			}
		}
	}
	return bases
}

// codeCommitsSince counts commits from base to HEAD that touch something other than memory.
func codeCommitsSince(root, base string) (int, error) {
	out, err := gitMem(root, append([]string{"rev-list", "--count", "--no-merges", base + "..HEAD", "--", "."}, memoryMetaPaths...)...)
	if err != nil {
		return 0, fmt.Errorf("count commits since %s: %s", shortSHA(base), out)
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// memoryDrift reports how far the code has moved since the overview was verified. ok is false
// when there is nothing to measure against (no overview, or it has never been committed).
func memoryDrift(root string) (base string, commits int, ok bool) {
	ov, has := currentOverview(root)
	if !has {
		return "", 0, false
	}
	base = verifiedAt(root, ov)
	if base == "" {
		return "", 0, false
	}
	n, err := codeCommitsSince(root, base)
	if err != nil {
		return "", 0, false
	}
	return base, n, true
}

// changedEvidence is a fact whose cited files changed after it was last verified.
type changedEvidence struct {
	Fact  fact
	Paths []string
}

// factCitedPaths are the files in changed that a fact names: file: sources, then paths in its body.
// Memory and the generated map files are never evidence; they change on every memory update.
func factCitedPaths(f fact, changed map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimPrefix(p, "./")
		if i := strings.IndexByte(p, ':'); i > 0 {
			p = p[:i] // path:line
		}
		p = strings.TrimSuffix(p, ".")
		if p != "" && !seen[p] && changed[p] && !isMemoryMetaPath(p) {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, s := range f.Sources {
		if p, ok := strings.CutPrefix(s, "file:"); ok {
			add(p)
		}
	}
	// Paths in the body: `internal/foo.go`, Makefile:12, .github/workflows/ci.yml. Unlike the
	// task-citation scan (citations.go) this takes bare root-level names too, because a candidate
	// counts only if it is a file that changed, so prose almost never matches.
	notPath := func(r rune) bool {
		return !(r == '/' || r == '.' || r == '_' || r == '-' || r == ':' || unicode.IsLetter(r) || unicode.IsDigit(r))
	}
	for _, w := range strings.FieldsFunc(f.Body, notPath) {
		add(w)
	}
	return out
}

// factsWithChangedEvidence lists facts a person or agent wrote whose cited files have changed
// since the fact was last committed. Derived facts are left to build, which re-derives them; the
// overview is covered by memoryDrift; questions cite nothing. Facts it has no time to check are
// left out rather than delaying the caller.
func factsWithChangedEvidence(root string, facts []fact, budget time.Duration) []changedEvidence {
	if headCommit(root) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	bases := factBases(ctx, root)
	changedSince := map[string]map[string]bool{}
	var out []changedEvidence
	for _, f := range facts {
		if f.Kind == "overview" || f.Kind == "question" || hasTag(f, derivedTag) {
			continue
		}
		rel, err := filepath.Rel(root, f.Path)
		if err != nil {
			continue
		}
		base := bases[filepath.ToSlash(rel)]
		if base == "" {
			continue
		}
		changed, ok := changedSince[base]
		if !ok {
			diff, err := gitDeadline(ctx, root, "diff", "--name-only", base, "HEAD")
			if err != nil {
				break // out of time, or git failed: report what is known
			}
			changed = map[string]bool{}
			for _, p := range strings.Split(diff, "\n") {
				changed[p] = true
			}
			changedSince[base] = changed
		}
		if paths := factCitedPaths(f, changed); len(paths) > 0 {
			out = append(out, changedEvidence{Fact: f, Paths: paths})
		}
	}
	return out
}

func isMemoryMetaPath(p string) bool {
	return strings.HasPrefix(p, "memory/") || p == "AGENTS.md" || p == "CLAUDE.md" || p == "GEMINI.md"
}

// printMemoryDrift is what build shows about staleness, before its plan.
func printMemoryDrift(root string, facts []fact) {
	if base, n, ok := memoryDrift(root); ok && n > 0 {
		fmt.Printf("the overview was last verified at %s; %d code commits since", shortSHA(base), n)
		if n >= memoryDriftThreshold {
			fmt.Print(" — `ptln memory build --apply --refresh` rewrites it from the last commit")
		}
		fmt.Println()
	}
	changed := factsWithChangedEvidence(root, facts, 30*time.Second)
	if len(changed) == 0 {
		return
	}
	fmt.Printf("%d facts cite files that changed since they were last committed:\n", len(changed))
	for _, c := range changed {
		fmt.Printf("  %s %-10s %s\n", c.Fact.ID, c.Fact.Kind, clipVis(c.Fact.Body, 70))
		fmt.Printf("  %s %-10s changed: %s\n", strings.Repeat(" ", len(c.Fact.ID)), "", strings.Join(c.Paths, ", "))
	}
	fmt.Println("check each against the code: `ptln memory edit` if wrong, `rm` if no longer true, `verify` if still right.")
	fmt.Println()
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// memoryVerify is `ptln memory verify <id>...`: the fact was checked against the code and still
// holds, so it is stamped with HEAD and stops being flagged.
func memoryVerify(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("usage: ptln memory verify <id>... (after checking each fact against the code)"))
	}
	ws, _ := workspaceHere()
	head := headCommit(ws.Dir)
	if head == "" {
		fatal(fmt.Errorf("verify needs a repository with commits that holds its own memory/"))
	}
	for _, id := range args {
		f, err := findFact(ws.Dir, id)
		if err != nil {
			fatal(err)
		}
		f.Verified = head
		if _, err := writeFact(ws.Dir, f); err != nil {
			fatal(err)
		}
		fmt.Printf("✓ %s verified at %s\n", f.ID, shortSHA(head))
	}
	refreshMemoryMap(ws)
}

// memoryDriftNotice is told to a session on its first turn when the code has moved
// memoryDriftThreshold commits past the overview. It is said once per threshold's worth of
// commits on this machine, so a user who says "not now" is asked again only after another 20.
func memoryDriftNotice(root string) string {
	base, n, ok := memoryDrift(root)
	if !ok || n < memoryDriftThreshold {
		return ""
	}
	head := headCommit(root)
	marker := filepath.Join(stateDir(), "memory-drift", filepath.Base(memoryOfferMarker(root)))
	if last, err := os.ReadFile(marker); err == nil {
		if since, err := codeCommitsSince(root, strings.TrimSpace(string(last))); err == nil && since < memoryDriftThreshold {
			return ""
		}
	}
	if os.MkdirAll(filepath.Dir(marker), 0o700) != nil || os.WriteFile(marker, []byte(head+"\n"), 0o600) != nil {
		return "" // no record means it would be said every session; better not at all
	}
	facts, _ := readFacts(root, false)
	changed := len(factsWithChangedEvidence(root, facts, 2*time.Second))
	return fmt.Sprintf(memoryDriftText, n, shortSHA(base), changed)
}

const memoryDriftText = `partyline: this repository's memory (memory/, summarised in AGENTS.md) was last verified %d code
commits ago, at %s, and %d facts cite files that have changed since. Products and code move on;
the memory does not update itself.

Finish the user's current request first. Then tell the user this in one sentence and ask once
whether to update the memory now. If they decline or do not answer, do not ask again. If they agree:
1. Run ` + "`ptln memory build --apply --refresh`" + `. It rewrites the overview from the last commit,
   starting from the current one, updates derived facts, and lists facts whose evidence changed.
2. Read the new overview against the code and correct it with ` + "`ptln memory edit <id> \"...\"`" + `.
3. Check each listed fact against the code: ` + "`ptln memory edit`" + ` if it is wrong, ` + "`ptln memory rm`" + `
   if it is no longer true, ` + "`ptln memory verify <id>`" + ` if it still holds. Record anything new
   the recent commits decided, with its reason and cost and the evidence.
4. Do not commit. Show ` + "`git status --short memory/ AGENTS.md`" + `.
`
