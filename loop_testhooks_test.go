package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary never touches the person's live tmux server from a run's state change; the
// indicator tests put the real function back against a scratch server.
func init() {
	// The test binary is also a contractor's runner in TestContractorsTmux: the window it opens
	// runs `<this binary> _contractor <id>` with the scratch server's HOME, before anything below
	// would move HOME.
	if len(os.Args) > 2 && os.Args[1] == "_contractor" {
		contractorMain(os.Args[2])
		os.Exit(0)
	}
	// TestLoopBuilderTmux's builder and rounds: the contractor window runs `loop _build`, which runs
	// the round as `loop _round`, each in its own process with nothing stubbed.
	if len(os.Args) > 2 && os.Args[1] == "loop" && (os.Args[2] == "_build" || os.Args[2] == "_round" || os.Args[2] == "_review") {
		loopMain(os.Args[2:])
		os.Exit(0)
	}
	// The contractor row's #() job: it only reads records and prints, so the e2e test can check
	// what tmux draws.
	if len(os.Args) == 3 && os.Args[1] == "tmux" && os.Args[2] == "--contractors-row" {
		fmt.Print(contractorRow(loadContractors(), contractorParentName, time.Now()))
		os.Exit(0)
	}
	// A scratch server's tmux.conf names this binary in its hooks and status rows (`tmux
	// --save-workspace`, `tmux --status …`). Run that way it would run the whole suite, whose tmux
	// tests kill the very server that ran it; it does nothing instead.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit(0)
	}
	// Every tmux call a test makes goes to a scratch server unless the test names its own socket
	// (t.Setenv overrides this). Without it a test that reaches tmuxCmd talked to the person's real
	// server: TestLauncherActions once sourced its temp-dir tmux.conf there, rebinding ctrl-\ to
	// the test binary, which is deleted when the run ends.
	if os.Getenv("PARTYLINE_TMUX_SOCKET") == "" {
		os.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-default")
	}
	// The same for state: a test that does not set HOME wrote into the person's ~/.partyline (a
	// batch from TestBatchApprovalRefusesARemovedConfig sat in their Loops tab for a day). Every
	// test starts in a scratch home; t.Setenv("HOME", …) still overrides it. Git still finds who
	// commits through the person's global config, which is read-only here.
	if real, err := os.UserHomeDir(); err == nil {
		scratch, _ := os.MkdirTemp("", "ptln-test-home-")
		if gc := filepath.Join(real, ".gitconfig"); fileExists(gc) {
			os.Setenv("GIT_CONFIG_GLOBAL", gc)
		}
		os.Setenv("HOME", scratch)
	}
	// The test binary never touches the person's menu bar: `ptln llms` (TestTmuxRequiredExit runs it)
	// wakes the tray, which SIGTERMs every running ptln-tray and starts one from the scratch HOME.
	wakeTray, tmuxBareWake = func() {}, func() {}
	markWaiting = func(*loopRun) {}
	spawnDecide = func(string) {} // the decider's own tests call loopDecide
	spawnBatchReview = func(string) error { return nil }
	spawnWatcher = func() error { return nil }
	realTmuxServerUp = tmuxServerUp
	tmuxServerUp = func() bool { return false } // the test binary never starts a watcher
	spawnScopeReview = scopeReviewRun           // in-process, so a test sees the outcome at once
	realBatchScopeCheck = batchScopeCheck
	batchScopeCheck = func(*loopRun, string, string) string { return "" }
	decisionProvenance = func() map[string]any { return map[string]any{"test": true} }
	spawnRound = func(id string, n int) error {
		spawnedRounds = append(spawnedRounds, fmt.Sprintf("%s/%d", id, n))
		return nil
	}
	// Batch steps are builder-driven: by default a test records the builder it would start and
	// commits nothing; TestLoopBuilder puts the real ones back.
	realStartLoopBuilder, realBuilderAdvance = startLoopBuilder, builderAdvance
	startLoopBuilder = func(r *loopRun, task string, reportOnly bool) error {
		startedBuilders = append(startedBuilders, task)
		r.Builder, r.Builds, r.BuilderEngine, r.ReportOnly = fmt.Sprintf("b%05d", r.Builds+1), r.Builds+1, "claude", reportOnly
		r.BuildRevision, r.BuilderStopped = r.Revision, false
		return nil
	}
	builderAdvance = func(string) {}
	spawnArbitrate = func(id string) error { arbitratePending(id); return nil } // in-process
	// Reviews run in-process unless a test puts the contractor back (TestReviewerContractor).
	realRunReviewerContractor = runReviewerContractor
	runReviewerContractor = func(ctx context.Context, _ reviewAt, e loopEngine, dir, prompt string) (string, loopTokens, error) {
		return runReviewer(ctx, e, dir, prompt)
	}
}

// startedBuilders records the tasks of the builders a test started.
var startedBuilders []string

var realStartLoopBuilder func(*loopRun, string, bool) error
var realRunReviewerContractor func(context.Context, reviewAt, loopEngine, string, string) (string, loopTokens, error)
var realBuilderAdvance func(string)

// spawnedRounds records the rounds a test started.
var spawnedRounds []string

// realBatchScopeCheck is the scope review before the stub, for the test that drives it with stubbed
// reviewers.
var realBatchScopeCheck func(*loopRun, string, string) string

// realTmuxServerUp is the server check before the stub, for the watcher's own-process test.
var realTmuxServerUp func() bool

// No test reaches the person's tmux server: the package default socket is a scratch one.
func TestTestsNeverUseTheLiveTmuxSocket(t *testing.T) {
	if got := tmuxSocketName(); got == "partyline" {
		t.Fatalf("tests run against the live socket %q", got)
	}
}

// No test writes into the person's home: the package default HOME is a scratch directory.
func TestTestsNeverUseTheRealHome(t *testing.T) {
	if h, _ := os.UserHomeDir(); !strings.Contains(h, "ptln-test-home-") && !strings.HasPrefix(h, os.TempDir()) && !strings.Contains(h, "/T/") {
		t.Fatalf("tests run with HOME %q", h)
	}
}
