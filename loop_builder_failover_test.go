package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// failoverRun is a building batch step whose approved builders are claude:a then codex:b, its
// contractor recorded as the current builder.
func failoverRun(t *testing.T, failover bool) (*loopRun, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	config := &loopConfig{Reviewers: [][]string{{"claude"}}, Failover: failover, Builder: []byte(`["claude:a","codex:b"]`)}
	b, r, err := createBatch(repo, []string{"pane:%7"}, []string{"true"}, "claude", []string{batchBrief("One", "Do one."), batchBrief("Two", "Do two.")}, config)
	if err != nil {
		t.Fatal(err)
	}
	applyBatchReview(r, b, []loopReview{{Engine: "claude"}})
	r, _ = loadLoopRun(r.ID)
	if err := loopApproveRun(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	r, _ = loadLoopRun(r.ID)
	loopBaselineRound(r)
	r, _ = loadLoopRun(r.ID)
	fakeContractor(t, contractor{ID: r.Builder, Loop: r.ID, Role: "builder", Engine: "claude:a", State: "running", Window: "@live"})
	_ = os.WriteFile(builderFile(r.ID, r.Builds, "prompt.md"), []byte("build it"), 0o600)
	return r, r.Builder
}

// The engines' limit messages, as the builder sees them: claude's in its stream-json result event,
// codex's as a turn.failed event. The wording is the captured reviewer samples (testdata/limits/);
// the event wrapping is claude's and codex's event formats (testdata/builder-claude-stream.jsonl).
func limitFailure(engine string, reset time.Time) error {
	if engine == "claude" {
		sample, _ := os.ReadFile("testdata/limits/claude-usage-limit.txt") // "...|<unix reset>"
		text, _, _ := strings.Cut(strings.TrimSpace(string(sample)), "|")
		var out strings.Builder
		_, failure, _ := renderBuilderStream("claude", strings.NewReader(fmt.Sprintf(`{"type":"result","subtype":"error","is_error":true,"result":%q}`, fmt.Sprintf("%s|%d", text, reset.Unix()))), &out)
		return &reviewerFailure{"claude builder: limited", failure}
	}
	sample, _ := os.ReadFile("testdata/limits/codex-capacity.txt")
	_, failure, _ := renderBuilderStream("codex", strings.NewReader(fmt.Sprintf(`{"type":"turn.failed","error":{"message":%q}}`, strings.TrimSpace(string(sample)))), io.Discard)
	return &reviewerFailure{"codex builder: limited", failure}
}

func TestBuilderFailover(t *testing.T) {
	t.Run("hands over", func(t *testing.T) {
		r, me := failoverRun(t, true)
		var prompts []string
		var used []string
		old := runBuilder
		t.Cleanup(func() { runBuilder = old })
		runBuilder = func(_ context.Context, e loopEngine, _, prompt, _ string, _ io.Writer) (string, loopTokens, error) {
			used, prompts = append(used, e.String()), append(prompts, prompt)
			if e.String() == "claude:a" {
				return "", loopTokens{In: 1200, Out: 300, Cached: 9000, CostUSD: 0.05}, limitFailure("claude", time.Now().Add(2*time.Hour))
			}
			return stubReport(""), loopTokens{In: 4000, Out: 800, Cached: 20000}, nil
		}
		text, e, err := runBuildFailover(r, r.Builds, me, "build it", io.Discard)
		if err != nil || e.String() != "codex:b" || validateReport(text) != nil {
			t.Fatalf("after a limit: %q from %s, %v", text, e, err)
		}
		if strings.Join(used, ",") != "claude:a,codex:b" || !strings.Contains(prompts[1], "previous builder hit its usage limit") || strings.Contains(prompts[0], "previous builder") {
			t.Errorf("builders %v, prompts %q", used, prompts)
		}
		// Both builders' tokens count, the limited one's too: it ran until the limit.
		var got []string
		for _, u := range loopUsages(r.ID) {
			got = append(got, fmt.Sprintf("%s %s build %d %d/%d/%d", u.Engine, u.Role, u.Build, u.Tokens.In, u.Tokens.Out, u.Tokens.Cached))
		}
		if want := fmt.Sprintf("claude:a builder build %d 1200/300/9000,codex:b builder build %d 4000/800/20000", r.Builds, r.Builds); strings.Join(got, ",") != want {
			t.Errorf("usage %q, want %q", got, want)
		}
		if until, _ := reviewerLimited("claude:a", time.Now()); until.IsZero() {
			t.Error("claude:a's limit was not recorded")
		}
		r2, _ := loadLoopRun(r.ID)
		c, _ := loadContractor(me)
		if r2.BuilderEngine != "codex:b" || c.Engine != "codex:b" || r2.State != loopBuilding || r2.ID != r.ID {
			t.Errorf("the same run carries on with codex:b: run %s %s, chip %s", r2.State, r2.BuilderEngine, c.Engine)
		}
		evs, _ := loopEvents(r.ID)
		if !hasEvent(evs, "builder_limited") || !hasEvent(evs, "builder_failover") {
			t.Error("the handover is not in the run's events")
		}
		// A limit already recorded is skipped from the start.
		used = nil
		if _, e, _ := runBuildFailover(r, r.Builds, me, "build it", io.Discard); e.String() != "codex:b" || len(used) != 1 {
			t.Errorf("a recorded limit was tried again: %v", used)
		}
	})
	t.Run("start picks the free builder", func(t *testing.T) {
		r, _ := failoverRun(t, true)
		_ = recordReviewerLimit("claude:a", time.Now().Add(time.Hour))
		old := startContractor
		t.Cleanup(func() { startContractor = old })
		var got *contractor
		startContractor = func(c *contractor) error { got = c; return nil }
		if err := realStartLoopBuilder(r, "build", false); err != nil {
			t.Fatal(err)
		}
		if got == nil || got.Engine != "codex:b" || got.Argv[5] != "codex:b" || r.BuilderEngine != "codex:b" {
			t.Errorf("with claude:a limited the builder started as %+v", got)
		}
		_ = recordReviewerLimit("codex:b", time.Now().Add(time.Hour)) // now every builder is limited
		if err := realStartLoopBuilder(r, "build", false); err != nil || got.Engine != "claude:a" {
			t.Errorf("with every builder limited the build starts as %+v (%v); it waits on the first", got, err)
		}
	})
	t.Run("codex limit", func(t *testing.T) {
		if _, ok := reviewerLimit("codex", limitFailure("codex", time.Time{}).(*reviewerFailure).diag, time.Now()); !ok {
			t.Error("codex's capacity message in a turn.failed event is not seen as a limit")
		}
		// The model's own answer quoting a limit message is not a limit.
		var out strings.Builder
		_, failure, _ := renderBuilderStream("claude", strings.NewReader(`{"type":"assistant","message":{"content":[{"type":"text","text":"Claude AI usage limit reached|1793170800"}]}}`), &out)
		if _, ok := reviewerLimit("claude", failure, time.Now()); ok {
			t.Error("the model's text was matched as a limit")
		}
	})
	t.Run("all limited waits then runs", func(t *testing.T) {
		r, me := failoverRun(t, true)
		_ = recordReviewerLimit("claude:a", time.Now().Add(time.Hour))
		_ = recordReviewerLimit("codex:b", time.Now().Add(30*time.Minute))
		oldWait, oldBuild := waitBuilderReset, runBuilder
		t.Cleanup(func() { waitBuilderReset, runBuilder = oldWait, oldBuild })
		var waited []time.Time
		waitBuilderReset = func(_ *loopRun, reset time.Time) bool {
			waited = append(waited, reset)
			_ = recordReviewerLimit("codex:b", time.Now().Add(-time.Minute)) // its limit lifts
			return true
		}
		runBuilder = func(context.Context, loopEngine, string, string, string, io.Writer) (string, loopTokens, error) {
			return stubReport(""), loopTokens{}, nil
		}
		_, e, err := runBuildFailover(r, r.Builds, me, "build it", io.Discard)
		if err != nil || e.String() != "codex:b" || len(waited) != 1 || waited[0].After(time.Now().Add(31*time.Minute)) {
			t.Errorf("waited for %v, then %s, %v", waited, e, err)
		}
		r, _ = loadLoopRun(r.ID)
		_ = recordReviewerLimit("claude:a", time.Now().Add(time.Hour))
		_ = recordReviewerLimit("codex:b", time.Now().Add(time.Hour))
		calls := 0
		waitBuilderReset = func(*loopRun, time.Time) bool { calls++; return true } // the limits never lift
		_, _, err = runBuildFailover(r, r.Builds, me, "build it", io.Discard)
		if _, ok := err.(errBuildersLimited); !ok || calls != maxReviewerWaits {
			t.Fatalf("after %d waits (want %d): %v", calls, maxReviewerWaits, err)
		}
		builderLimitedOut(r.ID, r.Builds, err.(errBuildersLimited))
		if r, _ = loadLoopRun(r.ID); r.State != loopFailed || !strings.Contains(r.Note, "usage limit") {
			t.Errorf("the step did not fail to the person: %s %q", r.State, r.Note)
		}
	})
	t.Run("past the horizon", func(t *testing.T) {
		r, me := failoverRun(t, true)
		_ = recordReviewerLimit("claude:a", time.Now().Add(20*time.Hour))
		_ = recordReviewerLimit("codex:b", time.Now().Add(20*time.Hour))
		old := waitBuilderReset
		t.Cleanup(func() { waitBuilderReset = old })
		waitBuilderReset = func(*loopRun, time.Time) bool { t.Error("waited past the horizon"); return true }
		if _, _, err := runBuildFailover(r, r.Builds, me, "build it", io.Discard); !errors.As(err, new(errBuildersLimited)) {
			t.Errorf("a 20-hour limit: %v", err)
		}
	})
	t.Run("failover off", func(t *testing.T) {
		r, me := failoverRun(t, false)
		_ = recordReviewerLimit("claude:a", time.Now().Add(time.Hour))
		oldWait, oldBuild := waitBuilderReset, runBuilder
		t.Cleanup(func() { waitBuilderReset, runBuilder = oldWait, oldBuild })
		waits := 0
		waitBuilderReset = func(*loopRun, time.Time) bool {
			waits++
			_ = recordReviewerLimit("claude:a", time.Now().Add(-time.Minute))
			return true
		}
		var used, prompt string
		runBuilder = func(_ context.Context, e loopEngine, _, p, _ string, _ io.Writer) (string, loopTokens, error) {
			used, prompt = e.String(), p
			return stubReport(""), loopTokens{}, nil
		}
		if _, _, err := runBuildFailover(r, r.Builds, me, "build it", io.Discard); err != nil || used != "claude:a" || waits != 1 || strings.Contains(prompt, "previous builder") {
			t.Errorf("failover off: waited %d, then ran %q (%v); codex:b must not be tried", waits, used, err)
		}
	})
	t.Run("card", func(t *testing.T) {
		r, _ := failoverRun(t, true)
		reset := time.Now().Add(40 * time.Minute).UTC()
		reviewEvent(r, "builders_waiting", map[string]any{"build": r.Builds, "reset": reset})
		if d := tabDetail(r); !strings.Contains(d, "⏸ resumes") || !strings.Contains(d, reset.Local().Format("15:04")) {
			t.Errorf("the card reads %q", d)
		}
		var row tabRow
		for _, x := range tabRows(time.Now()) {
			if x.RunID == r.ID {
				row = x
			}
		}
		st := gateStyle{tmuxTheme()}
		if g, _ := tabIndicatorGlyph(st.th, row, 0); g != "⏸" || !row.Reset.Equal(reset) || row.Builder != r.BuilderEngine ||
			!strings.Contains(gateANSI.ReplaceAllString(tabStateLine(st, row, 60), ""), "resumes "+reset.Local().Format("15:04")) {
			t.Errorf("the board's card: glyph %q, reset %v, builder %q", g, row.Reset, row.Builder)
		}
		reviewEvent(r, "builder_failover", map[string]any{"build": r.Builds, "model": "codex:b"})
		if d := tabDetail(r); strings.Contains(d, "⏸") {
			t.Errorf("the card still waits after a builder took over: %q", d)
		}
	})
}
