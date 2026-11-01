package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/clispec"
)

// `ptln loop` — partyline helps the session you are talking to build better. Step 1 of
// docs/plans/partyline-loops.md: `ptln loop review`, a cross-model review of the current work that
// needs no run, no supervisor and no tab. Hidden until the loop is validated.

const (
	loopReviewTimeout = 15 * time.Minute
	loopDiffCap       = 150 << 10 // bytes of diff put in the prompt; reviewers read the rest
)

func loopMain(args []string) {
	if len(args) > 0 && (args[0] == "approve" || args[0] == "decide" || args[0] == "stop") {
		fatal(fmt.Errorf("this command was removed; open the approval popup with ctrl-\\ then a"))
	}
	if len(args) > 0 && args[0] == clispec.LoopGuide {
		_, engine := engineAncestor()
		fmt.Print(renderLoopGuide(engine))
		return
	}
	ensureWatcher()
	if len(args) == 0 {
		printLoopHelp()
		return
	}
	switch args[0] {
	case "-h", "--help":
		printLoopHelp()
	case "tab":
		loopTab(indexOfArg(args, "--fixture") >= 0, args[1:]...)
	case "review":
		loopReviewCmd(args[1:])
	case clispec.LoopStart:
		loopStart(args[1:])
	case "brief":
		loopBrief(args[1:])
	case clispec.LoopReady:
		loopReady(args[1:])
	case "resume":
		loopResume(args[1:])
	case "delete", "rm":
		loopDeleteCmd(args[1:])
	case clispec.LoopDispute:
		loopDispute(args[1:])
	case "pr":
		loopPRCmd(args[1:])
	case clispec.LoopInbox:
		loopInboxCmd(args[1:])
	case clispec.LoopListen:
		loopListen(args[1:])
	case "status", "ls":
		loopStatus(args[1:])
	case "gate":
		loopGate()
	case "_review":
		loopReviewMain(args[1:])
	case "_decide":
		loopDecideMain(args[1:])
	case "_arbitrate":
		loopArbitrateMain(args[1:])
	case "_arbiter":
		loopArbiterMain(args[1:])
	case "_build":
		loopBuildMain(args[1:])
	case "_round":
		loopRoundMain(args[1:])
	case clispec.LoopBatch:
		loopBatchMain(args[1:])
	case "_batchreview":
		loopBatchReviewMain(args[1:])
	case "_scopereview":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: ptln loop _scopereview <id>"))
		}
		if err := scopeReviewRun(args[1]); err != nil {
			fatal(err)
		}
	default:
		fatal(fmt.Errorf("unknown loop command %q", args[0]))
	}
}

func loopReviewCmd(args []string) {
	by, base, brief, report := "claude,codex", "", "", ""
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 >= len(args) {
				fatal(fmt.Errorf("%s needs a value", args[i]))
			}
			i++
			return args[i]
		}
		switch args[i] {
		case "-h", "--help":
			printLoopHelp()
			return
		case "--by":
			by = next()
		case "--base":
			base = next()
		case "--brief":
			brief = readLoopFile(next())
		case "--report":
			report = readLoopFile(next())
		default:
			fatal(fmt.Errorf("unknown flag %q", args[i]))
		}
	}
	reviews, err := runLoopReview(by, base, brief, report)
	if err != nil {
		fatal(err)
	}
	// 0: every reviewer approved. 1: a P1, verified or not. 2: a reviewer failed. So a script, or a
	// session, cannot read a failed review as an approval.
	code := 0
	for _, r := range reviews {
		switch {
		case r.Error != "":
			code = 2
		case r.Verdict != "approve" && code == 0:
			code = 1
		}
	}
	os.Exit(code)
}

// runLoopReview does the work and returns, so the snapshot (a full copy of the working tree) is
// removed on every path; fatal exits without running deferred calls.
func runLoopReview(by, base, brief, report string) ([]loopReview, error) {
	engines, err := parseLoopEngines(by)
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()
	root, err := gitMem(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not in a git repository")
	}
	snap, err := takeSnapshot(root, base)
	if err != nil {
		return nil, err
	}
	defer snap.Cleanup()
	diff, err := snap.Diff()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(diff) == "" {
		fmt.Printf("nothing to review: no changes against %s\n", shortSHA(snap.Base))
		return nil, nil
	}
	out := filepath.Join(stateDir(), "loops", "reviews", time.Now().Format("20060102-150405")+"-"+filepath.Base(root))
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, err
	}
	prompt := loopReviewPrompt(brief, report, diff)
	_ = os.WriteFile(filepath.Join(out, "diff.patch"), []byte(diff), 0o600)
	_ = os.WriteFile(filepath.Join(out, "prompt.txt"), []byte(prompt), 0o600)

	fmt.Printf("reviewing %s against %s with %s (snapshot %s)\n", repoLabel(root), shortSHA(snap.Base), by, shortSHA(snap.Tree))
	// Untracked files go to the model providers too, and an unignored .env would be among them.
	// Name them before anything is sent.
	extra, err := snap.Untracked()
	if err != nil {
		return nil, fmt.Errorf("%v; not sending anything to the reviewers", err)
	}
	if len(extra) > 0 {
		fmt.Printf("includes %d untracked file(s): %s\n", len(extra), termSafe(strings.Join(extra, ", ")))
	}
	// The full patch goes into the snapshot, so a reviewer can read past the inline cap; the
	// export has no git history to recover it from.
	if err := snap.WritePatch(diff); err != nil {
		return nil, err
	}
	reviews := reviewSnapshot(snap, engines, prompt, diff, out, "", nil, nil)
	printLoopReviews(reviews)
	fmt.Printf("\nfull output: %s\n", out)
	return reviews, nil
}

func printLoopHelp() {
	if spec, ok := clispec.Lookup("loop"); ok {
		clispec.PrintHelp(os.Stdout, spec)
	}
}

func readLoopFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	return string(b)
}
