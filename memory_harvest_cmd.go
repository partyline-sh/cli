package main

// `ptln memory harvest` — read this repo's new commits and record what they declare.
//
// Run by hand to catch up, and by the watcher so nobody has to remember to. Harvesting is
// idempotent (fact ids come from the commit hash), so running it twice, or on two machines that
// both have the repo, writes the same files rather than duplicates.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/gitwt"
)

// harvestFirstRunCap bounds the first harvest of a repo. Without it, adding a five-year-old repo
// to a project would dump its whole history into a brief that shows 12 facts. Deliberate
// backfill is a separate, reviewed operation — see `--since`.
const harvestFirstRunCap = 50

// harvestMarkerPath remembers the last commit harvested from a repo. Machine-local: it is about
// what THIS clone has already read, not about the project, so it has no business in the shared
// memory repo.
func harvestMarkerPath(label, repoName string) string {
	safe := strings.NewReplacer("/", "_", string(os.PathSeparator), "_").Replace(repoName)
	return filepath.Join(stateDir(), "harvest", label, safe)
}

func readHarvestMarker(label, repo string) string {
	b, err := os.ReadFile(harvestMarkerPath(label, repo))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeHarvestMarker(label, repo, sha string) {
	p := harvestMarkerPath(label, repo)
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	_ = os.WriteFile(p, []byte(sha+"\n"), 0o600)
}

// harvestRepo reads new commits from one checkout and writes the facts they declare. Returns the
// facts written, so a caller can report them and decide whether to sync.
func harvestRepo(ws *projectWorkspace, repoPath, repoName, since string, dry bool) ([]fact, error) {
	rev, limit := since, 0
	if rev == "" {
		if mark := readHarvestMarker(ws.Label, repoName); mark != "" {
			rev = mark + "..HEAD"
		} else {
			limit = harvestFirstRunCap
		}
	}
	commits, err := readCommits(repoPath, rev, limit)
	if err != nil {
		// A marker pointing at a commit this clone no longer has (a rebase, a fresh clone) is
		// ordinary. Fall back to a bounded read rather than failing.
		if commits, err = readCommits(repoPath, "", harvestFirstRunCap); err != nil {
			return nil, err
		}
	}

	var wrote []fact
	for _, c := range commits {
		for _, f := range factsFromCommit(c, repoName) {
			if !dry {
				if _, err := writeFact(ws.Dir, f); err != nil {
					return wrote, err
				}
			}
			wrote = append(wrote, f)
		}
	}
	if !dry && len(commits) > 0 {
		writeHarvestMarker(ws.Label, repoName, commits[len(commits)-1].SHA)
	}
	return wrote, nil
}

// memoryHarvestMain is `ptln memory harvest [--since <rev>] [--dry-run]`.
func memoryHarvestMain(args []string) {
	since, dry := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 < len(args) {
				since, i = args[i+1], i+1
			}
		case "--dry-run", "-n":
			dry = true
		}
	}
	cwd, _ := os.Getwd()
	repo, err := gitwt.RepoRoot(cwd)
	if err != nil {
		fatal(fmt.Errorf("not a git repository — harvesting reads a repo's commits"))
	}
	ws, ok := projectForDir(cwd)
	if !ok {
		fatal(fmt.Errorf("this repo is not in a project — `ptln project add-repo <label>` first"))
	}
	name := ws.repoNameFor(cwd)
	if name == "" {
		name = filepath.Base(repo)
	}

	facts, err := harvestRepo(ws, repo, name, since, dry)
	if err != nil {
		fatal(err)
	}
	if len(facts) == 0 {
		fmt.Printf("nothing to record — no commit carried a %s trailer\n", trailerPrefix+"<kind>")
		fmt.Printf("  add one to a commit message:  %sGotcha: <what cost you time>\n", trailerPrefix)
		return
	}
	for _, f := range facts {
		fmt.Printf("  %-10s %s\n", f.Kind, clipVis(oneLine(f.Body), 66))
	}
	if dry {
		fmt.Printf("\n%d fact(s) would be recorded (dry run)\n", len(facts))
		return
	}
	fmt.Printf("\n%d fact(s) recorded\n", len(facts))
	if err := memorySync(ws.Dir, fmt.Sprintf("memory: %d fact(s) from %s commits", len(facts), name)); err != nil {
		fmt.Printf("recorded locally, not shared yet: %v\n", err)
	}
}
