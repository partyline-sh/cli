package main

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBatchConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"reviewers":[["codex:first","claude:opus"],["claude:sonnet"]],"builder":["claude:opus"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := readLoopConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Failover {
		t.Fatal("failover must default true")
	}
	b, r, err := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "Do one."), batchBrief("Two", "Do two.")}, config)
	if err != nil {
		t.Fatal(err)
	}
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	r, _ = loadLoopRun(r.ID)
	document := latestBrief(r)
	if !strings.Contains(document, "codex:first -> claude:opus") || !strings.Contains(document, "failover: true") || !strings.Contains(document, "builder: claude:opus") {
		t.Fatal(document)
	}
	m, _ := fakeGate(t, []*loopRun{r}, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	rendered := plain(nm.(gateModel))
	for _, want := range []string{"codex:first -> claude:opus", "slot 2: claude:sonnet", "failover: true"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("popup lacks %q:\n%s", want, rendered)
		}
	}
	changed := *config
	changed.Failover = false
	original := b.Config
	b.Config = &changed
	if briefHash(document) == briefHash(batchDocument(b, "No open P1.")) {
		t.Fatal("config not covered by document hash")
	}
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if err := loopApproveRun(r.ID, r.Revision); err == nil {
		t.Fatal("approved changed config")
	}
	b.Config = original
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if err := loopApproveRun(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	b, _ = loadBatch(b.ID)
	r, _ = loadLoopRun(r.ID)
	if b.ConfigHash != configHash(config) || configHash(r.Config) != configHash(config) {
		t.Fatalf("config not saved: %+v", b)
	}
	b.Config.Failover = false
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if err := r.checkConfig(); err == nil {
		t.Fatal("changed approved config accepted")
	}
	r.Config = b.Config
	if err := r.checkConfig(); err == nil {
		t.Fatal("config hash did not refuse matching edits to batch and run")
	}
	if err := r.set(loopPRReady, "", "pr_ready", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := batchNextStep(r); err == nil {
		t.Fatal("started next step with changed config")
	}
	for _, input := range []string{`{"reviewers":[]}`, `{"reviewers":[[]]}`, `{"reviewers":[["gemini:pro"]]}`, `{"reviewers":[["claude:"]]}`, `{"reviewers":[["claude:a b"]]}`} {
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readLoopConfig(path); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestFailoverReviewer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := runReviewer
	t.Cleanup(func() { runReviewer = old })
	var calls []string
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	runReviewer = func(_ context.Context, e loopEngine, _, _ string) (string, loopTokens, error) {
		calls = append(calls, e.String())
		if e.Model == "first" {
			return "", loopTokens{}, &reviewerFailure{"exit status 1", fmt.Sprintf("Claude AI usage limit reached|%d", reset.Unix())}
		}
		return `{"verdict":"approve","findings":[]}`, loopTokens{}, nil
	}
	slot := []string{"claude:first", "codex:second"}
	rv := reviewSlot(slot, true, t.TempDir(), "prompt", "", reviewAt{})
	if rv.Error != "" || rv.Engine != "codex:second" || !reflect.DeepEqual(calls, slot) || !reflect.DeepEqual(rv.Replaced, slot[:1]) {
		t.Fatalf("review %+v calls %v", rv, calls)
	}
	until, err := reviewerLimited(slot[0], time.Now())
	if err != nil || !until.Equal(reset) {
		t.Fatalf("reset %v err %v", until, err)
	}
	calls = nil
	rv = reviewSlot(slot, true, t.TempDir(), "prompt", "", reviewAt{})
	if rv.Engine != slot[1] || !reflect.DeepEqual(calls, slot[1:]) {
		t.Fatalf("did not skip: %+v %v", rv, calls)
	}
	if err := recordReviewerLimit(slot[1], reset.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	calls = nil
	rv = reviewSlot(slot, true, t.TempDir(), "prompt", "", reviewAt{})
	if rv.Error == "" || !rv.LimitedUntil.Equal(reset) || len(calls) != 0 {
		t.Fatalf("all limited: %+v %v", rv, calls)
	}
	if err := recordReviewerLimit(slot[0], time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	calls = nil
	rv = reviewSlot(slot, false, t.TempDir(), "prompt", "", reviewAt{})
	if rv.Error == "" || !reflect.DeepEqual(calls, slot[:1]) {
		t.Fatalf("disabled: %+v %v", rv, calls)
	}
	// Ordinary failures stop the slot rather than consuming another model.
	if err := recordReviewerLimit(slot[0], time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	runReviewer = func(_ context.Context, e loopEngine, _, _ string) (string, loopTokens, error) {
		calls = append(calls, e.String())
		return "", loopTokens{}, fmt.Errorf("ordinary error")
	}
	calls = nil
	rv = reviewSlot(slot, true, t.TempDir(), "prompt", "", reviewAt{})
	if rv.Error == "" || !rv.LimitedUntil.IsZero() || len(calls) != 1 {
		t.Fatalf("ordinary failure: %+v %v", rv, calls)
	}
}

func TestFailoverLimitSamples(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		engine, file string
		reset        time.Time
	}{
		{"codex", "codex-capacity.txt", now.Add(12 * time.Hour)},
		{"claude", "claude-usage-limit.txt", time.Unix(1793170800, 0)},
		// The wording of 2026-10-28, which went unrecognised and let rounds pass without claude.
		{"claude", "claude-hit-limit.txt", time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", "limits", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		reset, ok := reviewerLimit(tc.engine, string(data), now)
		if !ok || !reset.Equal(tc.reset) {
			t.Fatalf("%s: %v %t", tc.file, reset, ok)
		}
		other := "codex"
		if tc.engine == "codex" {
			other = "claude"
		}
		if _, ok := reviewerLimit(other, string(data), now); ok {
			t.Fatalf("%s sample matched %s", tc.engine, other)
		}
	}
	reset, ok := reviewerLimit("claude", "Claude AI usage limit reached; resets 3pm (UTC)", now)
	if !ok || !reset.Equal(time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("human reset: %v %t", reset, ok)
	}
	for _, engine := range []string{"claude", "codex"} {
		if _, ok := reviewerLimit(engine, "permission denied: ordinary reviewer error", now); ok {
			t.Fatal("ordinary error is a limit")
		}
	}
}

func TestFailoverRoundWaitsAndRecordsModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldRun, oldWait := runReviewer, waitReviewerReset
	t.Cleanup(func() { runReviewer = oldRun; waitReviewerReset = oldWait })
	config := &loopConfig{Reviewers: [][]string{{"codex:first", "claude:second"}}, Failover: true}
	r := &loopRun{ID: newLoopID(), State: loopChecking, Round: 1, Revision: 1, Config: config}
	if err := r.save(loopEvent{Type: "test"}); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	for _, model := range config.Reviewers[0] {
		if err := recordReviewerLimit(model, reset); err != nil {
			t.Fatal(err)
		}
	}
	waited := 0
	waitReviewerReset = func(got *loopRun, until time.Time) bool {
		waited++
		if got.ID != r.ID || !until.Equal(reset) {
			t.Fatalf("wrong wait: %s %v", got.ID, until)
		}
		for _, model := range config.Reviewers[0] {
			if err := recordReviewerLimit(model, time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		return true
	}
	runReviewer = func(_ context.Context, e loopEngine, _, _ string) (string, loopTokens, error) {
		if e.Name == "codex" {
			return "", loopTokens{}, &reviewerFailure{"exit 1", "ERROR: Selected model is at capacity. Please try a different model."}
		}
		return `{"verdict":"approve","findings":[]}`, loopTokens{}, nil
	}
	reviews := reviewRunSnapshot(r, &loopSnapshot{Dir: t.TempDir()}, nil, "prompt", "", "r1-")
	if waited != 1 || len(reviews) != 1 || reviews[0].Engine != "claude:second" || reviews[0].Error != "" {
		t.Fatalf("wait %d reviews %+v", waited, reviews)
	}
	events, err := loopEvents(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	waiting, failover := false, false
	for _, event := range events {
		waiting = waiting || event.Type == "reviewers_waiting"
		failover = failover || event.Type == "reviewer_failover"
	}
	if !waiting || !failover {
		t.Fatalf("events %+v", events)
	}
	summary := reviewModelsSummary(r)
	if !strings.Contains(summary, "r1-") || !strings.Contains(summary, "claude:second") {
		t.Fatal(summary)
	}
}

func TestFailoverLegacyReviewsUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := runReviewer
	t.Cleanup(func() { runReviewer = old })
	runReviewer = func(context.Context, loopEngine, string, string) (string, loopTokens, error) {
		return "", loopTokens{}, &reviewerFailure{"original reviewer failure", "ERROR: Selected model is at capacity. Please try a different model."}
	}
	engines, _ := parseLoopEngines("codex:first")
	reviews := reviewSnapshot(&loopSnapshot{Dir: t.TempDir()}, engines, "prompt", "", t.TempDir(), "", nil, nil)
	if len(reviews) != 1 || reviews[0].Error != "original reviewer failure" || !reviews[0].LimitedUntil.IsZero() {
		t.Fatalf("legacy reviews: %+v", reviews)
	}
	if _, err := os.Stat(filepath.Join(stateDir(), "limits.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy review wrote limits: %v", err)
	}
}

func TestFailoverAllFailedWaitsForAvailableReset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldRun, oldWait := runReviewer, waitReviewerReset
	t.Cleanup(func() { runReviewer = oldRun; waitReviewerReset = oldWait })
	r := &loopRun{ID: newLoopID(), State: loopChecking, Config: &loopConfig{Reviewers: [][]string{{"codex:error"}, {"claude:limited"}}, Failover: true}}
	if err := r.save(loopEvent{Type: "test"}); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	if err := recordReviewerLimit("claude:limited", reset); err != nil {
		t.Fatal(err)
	}
	runReviewer = func(context.Context, loopEngine, string, string) (string, loopTokens, error) {
		return "", loopTokens{}, fmt.Errorf("ordinary failure")
	}
	waited := false
	waitReviewerReset = func(_ *loopRun, until time.Time) bool {
		waited = true
		if !until.Equal(reset) {
			t.Fatalf("reset %v", until)
		}
		return false
	}
	reviews := reviewRunSnapshot(r, &loopSnapshot{Dir: t.TempDir()}, nil, "prompt", "", "r1-")
	if !waited || len(reviews) != 2 || reviews[0].Error == "" || reviews[1].Error == "" {
		t.Fatalf("waited %t: %+v", waited, reviews)
	}
}

// A review that succeeded is kept even when its findings quote a limit message (the code under
// review may well contain one); only the engine's own failure diagnostics are matched.
func TestFailoverIgnoresLimitTextInAReview(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := runReviewer
	t.Cleanup(func() { runReviewer = old })
	runReviewer = func(context.Context, loopEngine, string, string) (string, loopTokens, error) {
		return `{"verdict":"changes_required","findings":[{"priority":"P2","file":"loop_limits.go","line":1,"quote":"x","claim":"matches \"usage limit reached\" and \"Selected model is at capacity\"","fix":"y"}]}`, loopTokens{}, nil
	}
	for _, model := range []string{"claude:a", "codex:b"} {
		rv := reviewSlot([]string{model}, true, t.TempDir(), "prompt", "", reviewAt{})
		if rv.Error != "" || !rv.LimitedUntil.IsZero() || len(rv.Findings)+len(rv.UnverifiedClaims)+len(rv.Dropped) != 1 {
			t.Errorf("%s: a review quoting a limit was treated as one: %+v", model, rv)
		}
	}
	// A failure whose message is the model's text, not the engine's diagnostics, is not a limit.
	runReviewer = func(context.Context, loopEngine, string, string) (string, loopTokens, error) {
		return "usage limit reached|1793170800", loopTokens{}, &reviewerFailure{"exit status 1", "panic: something else"}
	}
	if rv := reviewSlot([]string{"claude:a"}, true, t.TempDir(), "prompt", "", reviewAt{}); !rv.LimitedUntil.IsZero() {
		t.Errorf("the answer text was matched as a limit: %+v", rv)
	}
	if _, err := os.Stat(filepath.Join(stateDir(), "limits.json")); !os.IsNotExist(err) {
		t.Errorf("limits.json was written: %v", err)
	}
}

// A reset already past or absurdly far away is held for 12 hours instead: no hot retry loop, no
// model banned for years.
func TestFailoverResetIsClamped(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for _, ts := range []int64{now.Add(-time.Hour).Unix(), 9999999999} {
		reset, ok := reviewerLimit("claude", fmt.Sprintf("Claude AI usage limit reached|%d", ts), now)
		if !ok || !reset.Equal(now.Add(12*time.Hour)) {
			t.Errorf("reset %d became %v", ts, reset)
		}
	}
	week := now.Add(6 * 24 * time.Hour)
	if reset, _ := reviewerLimit("claude", fmt.Sprintf("Claude AI usage limit reached|%d", week.Unix()), now); !reset.Equal(week) {
		t.Errorf("a weekly reset was clamped: %v", reset)
	}
}

// A round stops waiting after maxReviewerWaits limits in a row.
func TestFailoverWaitsAreCapped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldRun, oldWait := runReviewer, waitReviewerReset
	t.Cleanup(func() { runReviewer = oldRun; waitReviewerReset = oldWait })
	r := &loopRun{ID: newLoopID(), State: loopChecking, Config: &loopConfig{Reviewers: [][]string{{"codex:a"}}, Failover: true}}
	if err := r.save(loopEvent{Type: "test"}); err != nil {
		t.Fatal(err)
	}
	runs, waits := 0, 0
	runReviewer = func(context.Context, loopEngine, string, string) (string, loopTokens, error) {
		runs++
		return "", loopTokens{}, &reviewerFailure{"exit 1", "ERROR: Selected model is at capacity."}
	}
	waitReviewerReset = func(*loopRun, time.Time) bool {
		waits++
		_ = recordReviewerLimit("codex:a", time.Now().Add(-time.Second))
		return true
	}
	reviews := reviewRunSnapshot(r, &loopSnapshot{Dir: t.TempDir()}, nil, "prompt", "", "r1-")
	if waits != maxReviewerWaits || runs != maxReviewerWaits+1 || reviews[0].Error == "" {
		t.Errorf("waits %d runs %d: %+v", waits, runs, reviews)
	}
}

// The real wait: a passed reset returns at once; a run that moves on stops the wait.
func TestWaitReviewerReset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := &loopRun{ID: newLoopID(), State: loopChecking, Round: 1, Revision: 1}
	if err := r.save(loopEvent{Type: "test"}); err != nil {
		t.Fatal(err)
	}
	if !waitReviewerReset(r, time.Now().Add(-time.Minute)) {
		t.Error("a passed reset did not return true")
	}
	if !waitReviewerReset(r, time.Now().Add(50*time.Millisecond)) {
		t.Error("a near reset did not return true")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		moved := *r
		moved.State = loopStopped
		_ = moved.save(loopEvent{Type: "stopped"})
	}()
	start := time.Now()
	if waitReviewerReset(r, time.Now().Add(time.Hour)) {
		t.Error("a stopped run's wait returned true")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the stop took %v to notice", time.Since(start))
	}
}

// Clearing the config after review does not pass against a document that still shows one.
func TestBatchApprovalRefusesARemovedConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	config := &loopConfig{Reviewers: [][]string{{"claude:a"}}, Failover: true}
	b, r, err := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "Do one."), batchBrief("Two", "Do two.")}, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := withBatch(b.ID, func(b *loopBatch) error { b.Config = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	r.Config = nil
	if _, err := batchApproved(r); err == nil || !strings.Contains(err.Error(), "config changed") {
		t.Errorf("approval with the config removed: %v", err)
	}
}

// --config reaches the run through the flags, the only way a person sets it.
func TestBatchConfigFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	brief := filepath.Join(dir, "brief.md")
	_ = os.WriteFile(path, []byte(`{"reviewers":[["codex:a","claude:b"]],"failover":false}`), 0o600)
	_ = os.WriteFile(brief, []byte("brief"), 0o600)
	f, _ := parseLoopFlags([]string{"--brief", brief, "--checks", "true", "--config", path})
	if f.config == nil || !reflect.DeepEqual(f.config.Reviewers, [][]string{{"codex:a", "claude:b"}}) || f.config.Failover {
		t.Fatalf("--config gave %+v", f.config)
	}
}

// A brief's own "Model config" heading is demoted inside the batch document, so a batch without a
// config still approves, and the heading cannot stand in for the real section.
func TestBatchConfigSectionIsTheDocuments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	own := batchBrief("One", "Do one.") + "\n## Model config\nslot 1: claude:x\nfailover: true\n"
	b, r, err := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{own, batchBrief("Two", "Do two.")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	r, _ = loadLoopRun(r.ID)
	if _, err := batchApproved(r); err != nil {
		t.Errorf("a config-less batch whose brief has a Model config heading: %v", err)
	}
}

// A ten-digit number after some other pipe in the diagnostics is not a reset.
func TestFailoverResetNeedsTheLimitPhrase(t *testing.T) {
	now := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	stray := now.Add(3 * 24 * time.Hour).Unix()
	reset, ok := reviewerLimit("claude", fmt.Sprintf("log | %d\nrate limit reached", stray), now)
	if !ok || !reset.Equal(now.Add(12*time.Hour)) {
		t.Errorf("a stray number set the reset: %v", reset)
	}
}
