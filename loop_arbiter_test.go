package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// arbiterBatch is an approved batch whose disputes go to the arbiter (or, with ask, to the person),
// its first step building at round 1 with one open P1, F1-1.
func arbiterBatch(t *testing.T, disputes string) *loopRun {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo, _ := loopRepo(t)
	config := &loopConfig{Reviewers: [][]string{{"claude"}}, Failover: true, Disputes: disputes}
	if disputes == "arbiter" {
		config.Arbiter = "codex:gpt-5"
	}
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
	r.Round = 1
	_ = r.save(loopEvent{Type: "test"})
	writeLoopJSON(r.ID, "findings-1.json", []loopTracked{{ID: "F1-1", Engine: "claude",
		loopFinding: loopFinding{Priority: "P1", File: "main.go", Line: 1, Quote: "package main", Claim: "main is wrong", Fix: "change it"}}})
	return r
}

// stubArbiter answers every ruling with ruling and counts the calls.
func stubArbiter(t *testing.T, ruling arbiterRuling) *int {
	t.Helper()
	calls := 0
	old := runArbiter
	t.Cleanup(func() { runArbiter = old })
	runArbiter = func(r *loopRun, f loopTracked) arbiterRuling {
		calls++
		if f.Dispute == "" || r.Config.Arbiter == "" {
			t.Errorf("the arbiter got no dispute or engine: %+v", f)
		}
		return ruling
	}
	return &calls
}

func TestArbiter(t *testing.T) {
	t.Run("confident dismiss", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		calls := stubArbiter(t, arbiterRuling{Decision: "dismiss", Confidence: 0.9, Reason: "main.go is fine"})
		if _, err := loopRaiseDispute(r.ID, "F1-1", "not a bug"); err != nil {
			t.Fatal(err)
		}
		if b, _ := loadBatch(r.Batch); len(b.Deferred) != 0 {
			t.Fatal("a dispute for the arbiter was deferred to the person before it ruled")
		}
		lines := arbitratePending(r.ID)
		r, _ = loadLoopRun(r.ID)
		b, _ := loadBatch(r.Batch)
		var decided []loopTracked
		readLoopJSON(r.ID, "decided.json", &decided)
		if *calls != 1 || len(decided) != 1 || decided[0].Decision != "dismiss" || !r.Recheck || len(b.Deferred) != 0 {
			t.Fatalf("calls %d, decided %+v, recheck %v, deferred %v", *calls, decided, r.Recheck, b.Deferred)
		}
		if a := b.Arbitrated; len(a) != 1 || a[0].Decision != "dismiss" || a[0].Confidence != 0.9 || a[0].Bar != arbiterBar || a[0].Reason != "main.go is fine" || a[0].Engine != "codex:gpt-5" {
			t.Errorf("ruling record: %+v", b.Arbitrated)
		}
		if len(lines) != 1 || !strings.Contains(lines[0], "ruled dismiss") {
			t.Errorf("told %v", lines)
		}
		if evs, _ := loopEvents(r.ID); !hasEvent(evs, "arbiter_ruling") {
			t.Error("the ruling is not in the run's events")
		}
		if s := batchSummary(b); !strings.Contains(s, "Ruled by the arbiter") || !strings.Contains(s, "main.go is fine") || !strings.Contains(s, "confidence 0.90, bar 0.80") {
			t.Errorf("the summary lacks the ruling:\n%s", s)
		}
		// A dismissed finding is settled: the next review does not raise it again.
		if !sameFinding(settled(decided), decided[0].loopFinding) {
			t.Error("the dismissed finding is not settled for later reviews")
		}
	})
	for name, ruling := range map[string]arbiterRuling{
		"unsure":        {Decision: "dismiss", Confidence: 0.6, Reason: "maybe"},
		"invalid":       {Err: "no JSON object in the reply"},
		"confident fix": {Decision: "fix", Confidence: 0.95, Reason: "it is a bug"},
	} {
		t.Run(name, func(t *testing.T) {
			r := arbiterBatch(t, "arbiter")
			stubArbiter(t, ruling)
			if _, err := loopRaiseDispute(r.ID, "F1-1", "not a bug"); err != nil {
				t.Fatal(err)
			}
			arbitratePending(r.ID)
			b, _ := loadBatch(r.Batch)
			var decided []loopTracked
			readLoopJSON(r.ID, "decided.json", &decided)
			if name == "confident fix" {
				if len(b.Deferred) != 0 || len(decided) != 1 || decided[0].Decision != "fix" {
					t.Errorf("a confident fix: deferred %v, decided %+v", b.Deferred, decided)
				}
				return
			}
			if len(b.Deferred) != 1 || b.Deferred[0].ID != "S1-F1-1" || b.Arbitrated[0].Decision != "" {
				t.Errorf("%s did not reach the person: deferred %+v, ruling %+v", name, b.Deferred, b.Arbitrated)
			}
		})
	}
	t.Run("ask me", func(t *testing.T) {
		r := arbiterBatch(t, "ask")
		calls := stubArbiter(t, arbiterRuling{Decision: "dismiss", Confidence: 1, Reason: "x"})
		if _, err := loopRaiseDispute(r.ID, "F1-1", "not a bug"); err != nil {
			t.Fatal(err)
		}
		arbitratePending(r.ID)
		if b, _ := loadBatch(r.Batch); *calls != 0 || len(b.Deferred) != 1 || len(b.Arbitrated) != 0 {
			t.Errorf("ask me: arbiter calls %d, deferred %v", *calls, b.Deferred)
		}
	})
	t.Run("reverse", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		stubArbiter(t, arbiterRuling{Decision: "dismiss", Confidence: 0.9, Reason: "fine"})
		_, _ = loopRaiseDispute(r.ID, "F1-1", "not a bug")
		arbitratePending(r.ID)
		id, err := batchReverseDismiss(r.Batch)
		b, _ := loadBatch(r.Batch)
		if err != nil || id != "S1-F1-1" || len(b.Deferred) != 1 || b.Deferred[0].Decision != "deferred" || !b.Arbitrated[0].Reversed ||
			!strings.Contains(b.Deferred[0].Dispute, "the arbiter dismissed it") {
			t.Fatalf("reverse: %q %v, deferred %+v", id, err, b.Deferred)
		}
		if r2, _ := loadLoopRun(r.ID); r2.State != loopBuilding || b.Deferral != "" {
			t.Errorf("reversing reopened the step or ended the batch: %s, deferral %q", r2.State, b.Deferral)
		}
		if _, err := batchReverseDismiss(r.Batch); err == nil {
			t.Error("a second reverse found a dismissal to reverse")
		}
	})
	t.Run("reverse after the batch", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		stubArbiter(t, arbiterRuling{Decision: "dismiss", Confidence: 0.9, Reason: "fine"})
		_, _ = loopRaiseDispute(r.ID, "F1-1", "not a bug")
		arbitratePending(r.ID)
		_ = withBatch(r.Batch, func(b *loopBatch) error { b.Stopped = true; return nil })
		_ = withRunLock(r.ID, func(cur *loopRun) error { return cur.set(loopStopped, "stopped", "stopped", nil) })
		if _, err := batchReverseDismiss(r.Batch); err != nil {
			t.Fatal(err)
		}
		b, _ := loadBatch(r.Batch)
		d, err := loadLoopRun(b.Deferral)
		if err != nil || d.State != loopAwaitYou {
			t.Fatalf("a reverse after the batch ended did not put it to the person: %+v %v", d, err)
		}
		// Its "fix" rulings are not "fixed" until a deferred review has committed them.
		_ = withBatch(r.Batch, func(b *loopBatch) error { b.Deferred[0].Decision = "fix"; return nil })
		if b, _ = loadBatch(r.Batch); b.Deferred[0].Decision != "fix" {
			t.Fatal("setup")
		}
		// While that review's fixes are being built, a reverse waits.
		_ = withRunLock(d.ID, func(cur *loopRun) error { return cur.set(loopBuilding, "fixing", "test", nil) })
		_ = withBatch(r.Batch, func(b *loopBatch) error {
			b.Arbitrated = append(b.Arbitrated, loopArbitration{Step: 1, Finding: loopTracked{ID: "F1-2"}, Decision: "dismiss"})
			return nil
		})
		before := b.Deferral
		if _, err := batchReverseDismiss(r.Batch); err != nil {
			t.Errorf("a reverse during the deferred fixes: %v", err)
		}
		if b, _ = loadBatch(r.Batch); b.Deferral != before || b.Deferred[len(b.Deferred)-1].Decision != "deferred" {
			t.Errorf("during the fixes a reverse is listed for after them, not a second review: deferral %s→%s", before, b.Deferral)
		}
		if _, err := batchReverse(r.Batch, "S1-F1-2"); err == nil || !strings.Contains(err.Error(), "no longer") {
			t.Errorf("a stale confirmation: %v", err)
		}
		// A finished batch with a standing dismissal stays in the popup while its branch exists.
		_ = withBatch(r.Batch, func(b *loopBatch) error {
			b.Arbitrated = append(b.Arbitrated, loopArbitration{Step: 1, Finding: loopTracked{ID: "F1-3"}, Decision: "dismiss"})
			return nil
		})
		r, _ = loadLoopRun(r.ID)
		if !batchBranchExists(r) {
			t.Error("the batch's branch is not found")
		}
		if s := m2(t, r); !strings.Contains(s, "FINISHED BATCH") || strings.Contains(s, "s stop") || !strings.Contains(s, "x reverse") {
			t.Errorf("a finished batch's screen:\n%s", s)
		}
	})
	t.Run("popup", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		stubArbiter(t, arbiterRuling{Decision: "dismiss", Confidence: 0.9, Reason: "fine"})
		_, _ = loopRaiseDispute(r.ID, "F1-1", "not a bug")
		arbitratePending(r.ID)
		r, _ = loadLoopRun(r.ID)
		m, _ := fakeGate(t, []*loopRun{r}, nil)
		reversed := 0
		m.act.reverse = func(_ *loopRun, which string) (string, error) {
			if which != "S1-F1-1" {
				t.Errorf("the popup reversed %q, not the one it showed", which)
			}
			reversed++
			return which, nil
		}
		nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
		m = nm.(gateModel)
		m = gateKeys(m, "enter")
		if s := plain(m); !strings.Contains(s, "DISMISSED BY THE ARBITER") || !strings.Contains(s, "S1-F1-1 (x reverses)") || !strings.Contains(s, "x reverse a dismiss") {
			t.Fatalf("the popup does not show the dismissal:\n%s", s)
		}
		m = gateKeys(m, "x")
		if reversed != 0 {
			t.Fatal("x reversed without the code")
		}
		m = gateKeys(m, strings.Split(m.code, "")...)
		if reversed != 1 {
			t.Errorf("the code did not reverse it (reversed %d)\n%s", reversed, plain(m))
		}
	})
	t.Run("builder", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		calls := stubArbiter(t, arbiterRuling{Decision: "dismiss", Confidence: 0.9, Reason: "fine"})
		before := len(spawnedRounds)
		loopBuilderDone(r.ID, r.Builds, stubReport("## Disputes\nF1-1: not a bug\n"), nil)
		r, _ = loadLoopRun(r.ID)
		if *calls != 1 || r.State != loopChecking || len(spawnedRounds) != before+1 {
			t.Errorf("a builder's dispute: arbiter calls %d, state %s, rounds %v", *calls, r.State, spawnedRounds[before:])
		}
	})
	t.Run("stopped while ruling", func(t *testing.T) {
		for _, ruling := range []arbiterRuling{{Decision: "dismiss", Confidence: 0.9, Reason: "fine"}, {Decision: "fix", Confidence: 0.3, Reason: "unsure"}} {
			r := arbiterBatch(t, "arbiter")
			old := runArbiter
			runArbiter = func(r *loopRun, f loopTracked) arbiterRuling {
				if err := loopStopRun(r.ID); err != nil { // the person stops the batch meanwhile
					t.Error(err)
				}
				return ruling
			}
			_, _ = loopRaiseDispute(r.ID, "F1-1", "not a bug")
			arbitratePending(r.ID)
			runArbiter = old
			r, _ = loadLoopRun(r.ID)
			b, _ := loadBatch(r.Batch)
			if r.State != loopStopped || len(b.Arbitrated) != 1 {
				t.Errorf("a ruling after the stop: state %s, rulings %d", r.State, len(b.Arbitrated))
			}
			if ruling.Confidence < arbiterBar {
				d, err := loadLoopRun(b.Deferral)
				if len(b.Deferred) != 1 || b.Deferred[0].ID != "S1-F1-1" || err != nil || d.State != loopAwaitYou {
					t.Fatalf("an unsure ruling after the stop is not put to the person: deferred %+v, review %+v %v", b.Deferred, d, err)
				}
				// And it can be decided there.
				if err := loopDecideDispute(d.ID, "S1-F1-1", "dismiss"); err != nil {
					t.Errorf("deciding it: %v", err)
				}
			}
		}
	})
	t.Run("no round while pending", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		_, _ = loopRaiseDispute(r.ID, "F1-1", "not a bug")
		if _, err := loopRaiseDispute(r.ID, "F1-1", "again"); err == nil || !strings.Contains(err.Error(), "already waiting") {
			t.Errorf("the same finding was queued twice: %v", err)
		}
		if _, err := loopStartRound(r.ID, stubReport("")); err == nil || !strings.Contains(err.Error(), "has not ruled") {
			t.Errorf("a round started with a ruling pending: %v", err)
		}
	})
	t.Run("one claim", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		var mu sync.Mutex
		calls := 0
		old := runArbiter
		t.Cleanup(func() { runArbiter = old })
		runArbiter = func(*loopRun, loopTracked) arbiterRuling {
			mu.Lock()
			calls++
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			return arbiterRuling{Decision: "dismiss", Confidence: 0.9, Reason: "fine"}
		}
		_, _ = loopRaiseDispute(r.ID, "F1-1", "not a bug")
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); arbitratePending(r.ID) }()
		}
		wg.Wait()
		if b, _ := loadBatch(r.Batch); calls != 1 || len(b.Arbitrated) != 1 {
			t.Errorf("two processes ruled: calls %d, rulings %d", calls, len(b.Arbitrated))
		}
		// A claim by a process that has gone is taken again.
		_ = withRunLock(r.ID, func(cur *loopRun) error {
			return arbiterQueue(cur, loopTracked{ID: "F1-1", Dispute: "again", loopFinding: loopFinding{Priority: "P1", Claim: "c"}})
		})
		p := pendingRulings(r.ID)
		p[0].Claimed = 1 << 22 // no such process
		writeLoopJSON(r.ID, arbiterPendingFile, p)
		arbitratePending(r.ID)
		if calls != 2 || len(pendingRulings(r.ID)) != 0 {
			t.Errorf("a stale claim was not taken again: calls %d, pending %v", calls, pendingRulings(r.ID))
		}
	})
	t.Run("contractor path", func(t *testing.T) {
		for reply, want := range map[string]string{
			`{"decision":"dismiss","confidence":0.9,"reason":"fine"}`: "dismiss",
			"I think it is fine.": "",  // no valid answer: to the person
			"":                    "-", // the arbiter leaves no ruling
		} {
			r := arbiterBatch(t, "arbiter")
			oldStart, oldReviewer := startContractor, runReviewer
			runReviewer = func(context.Context, loopEngine, string, string) (string, loopTokens, error) {
				return reply, loopTokens{}, nil
			}
			startContractor = func(c *contractor) error {
				if c.Role != "arbiter" || c.Engine != "codex:gpt-5" || c.Argv[2] != "_arbiter" {
					t.Errorf("arbiter contractor: %+v", c)
				}
				c.State, c.Started = "running", time.Now().UTC()
				_ = withContractors(c.save)
				go func() {
					if reply != "" {
						loopArbiterMain(c.Argv[3:])
					}
					finishContractor(c.ID, nil, "")
				}()
				return nil
			}
			_, _ = loopRaiseDispute(r.ID, "F1-1", "not a bug")
			arbitratePending(r.ID)
			startContractor, runReviewer = oldStart, oldReviewer
			b, _ := loadBatch(r.Batch)
			got := b.Arbitrated[0].Decision
			if want == "-" {
				if got != "" || !strings.Contains(b.Arbitrated[0].Reason, "left no ruling") {
					t.Errorf("no ruling left: %+v", b.Arbitrated[0])
				}
			} else if got != want || (want == "" && len(b.Deferred) != 1) {
				t.Errorf("reply %q: ruled %q, deferred %v", reply, got, b.Deferred)
			}
		}
	})
	t.Run("fixed only when committed", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		stubArbiter(t, arbiterRuling{Decision: "dismiss", Confidence: 0.9, Reason: "fine"})
		_, _ = loopRaiseDispute(r.ID, "F1-1", "not a bug")
		arbitratePending(r.ID)
		// A stopped batch's deferred review recorded a fix ruling and built nothing.
		_ = withBatch(r.Batch, func(b *loopBatch) error {
			b.Stopped = true
			b.Deferred = append(b.Deferred, loopTracked{ID: "S1-F9-9", Decision: "fix"})
			return nil
		})
		_ = withRunLock(r.ID, func(cur *loopRun) error { return cur.set(loopStopped, "stopped", "stopped", nil) })
		if _, err := batchReverseDismiss(r.Batch); err != nil {
			t.Fatal(err)
		}
		b, _ := loadBatch(r.Batch)
		for _, d := range b.Deferred {
			if d.ID == "S1-F9-9" && d.Decision != "fix" {
				t.Errorf("a fix nobody built is %q", d.Decision)
			}
		}
	})
	t.Run("builder told to fix", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		stubArbiter(t, arbiterRuling{Decision: "fix", Confidence: 0.9, Reason: "it is a bug"})
		before, builds := len(spawnedRounds), r.Builds
		loopBuilderDone(r.ID, r.Builds, stubReport("## Disputes\nF1-1: not a bug\n"), nil)
		r, _ = loadLoopRun(r.ID)
		if r.State != loopBuilding || r.Builds != builds+1 || len(spawnedRounds) != before || !strings.Contains(startedBuilders[len(startedBuilders)-1], "ruled that these findings you disputed must be fixed") {
			t.Errorf("a confident fix: state %s, builds %d, rounds %v", r.State, r.Builds, spawnedRounds[before:])
		}
	})
	t.Run("takeover", func(t *testing.T) {
		// A claimer that died left its arbiter running: the next one waits for that arbiter.
		r := arbiterBatch(t, "arbiter")
		_ = withRunLock(r.ID, func(cur *loopRun) error {
			return arbiterQueue(cur, loopTracked{ID: "F1-1", Dispute: "not a bug", loopFinding: loopFinding{Priority: "P1", Claim: "c"}})
		})
		fakeContractor(t, contractor{ID: "abcdef01", Loop: r.ID, Role: "arbiter", State: "running", Window: "@live"})
		p := pendingRulings(r.ID)
		p[0].Claimed, p[0].Contractor = 1<<22, "abcdef01"
		writeLoopJSON(r.ID, arbiterPendingFile, p)
		old := startContractor
		t.Cleanup(func() { startContractor = old })
		startContractor = func(*contractor) error { t.Error("a second arbiter was started"); return nil }
		go func() {
			time.Sleep(300 * time.Millisecond)
			writeLoopJSON(r.ID, "arbiter-abcdef01.json", arbiterRuling{Decision: "dismiss", Confidence: 0.95, Reason: "from the first arbiter"})
			finishContractor("abcdef01", nil, "")
		}()
		arbitratePending(r.ID)
		if b, _ := loadBatch(r.Batch); len(b.Arbitrated) != 1 || b.Arbitrated[0].Reason != "from the first arbiter" {
			t.Errorf("the takeover's ruling: %+v", b.Arbitrated)
		}
	})
	t.Run("ruling", func(t *testing.T) {
		for text, ok := range map[string]bool{
			`{"decision":"dismiss","confidence":0.85,"reason":"fine"}`:    true,
			`Here: {"decision":"fix","confidence":1,"reason":"bug"} done`: true,
			`{"decision":"maybe","confidence":0.9,"reason":"x"}`:          false,
			`{"decision":"fix","confidence":1.5,"reason":"x"}`:            false,
			`{"decision":"fix","confidence":0.9,"reason":""}`:             false,
			`no json`: false,
		} {
			if got := parseArbiterRuling(text); (got.Err == "") != ok {
				t.Errorf("%s: %+v", text, got)
			}
		}
		p := arbiterPrompt("# One\nthe brief", loopTracked{ID: "F1-1", Dispute: "not a bug", loopFinding: loopFinding{Claim: "main is wrong"}})
		for _, want := range []string{"the brief", "F1-1", "main is wrong", "not a bug", "read-only snapshot", "below 0.8"} {
			if !strings.Contains(p, want) {
				t.Errorf("the arbiter's prompt lacks %q", want)
			}
		}
	})
	t.Run("read-only run", func(t *testing.T) {
		r := arbiterBatch(t, "arbiter")
		_ = os.WriteFile(filepath.Join(r.Repo, "disputed.go"), []byte("package main // as disputed\n"), 0o644)
		_ = withRunLock(r.ID, func(cur *loopRun) error {
			return arbiterQueue(cur, loopTracked{ID: "F1-1", Dispute: "not a bug"})
		})
		// The work changes after the dispute: the arbiter still reads it as disputed.
		_ = os.WriteFile(filepath.Join(r.Repo, "disputed.go"), []byte("package main // changed later\n"), 0o644)
		_ = os.WriteFile(loopFile(r.ID, "arbiter-F1-1.prompt.md"), []byte("rule"), 0o600)
		var read string
		old := runReviewer
		t.Cleanup(func() { runReviewer = old })
		var engine loopEngine
		runReviewer = func(_ context.Context, e loopEngine, dir, prompt string) (string, loopTokens, error) {
			engine = e
			b, _ := os.ReadFile(filepath.Join(dir, "disputed.go"))
			read = string(b)
			return `{"decision":"fix","confidence":0.9,"reason":"bug"}`, loopTokens{}, nil
		}
		if got := arbitrate(r, "F1-1"); got.Decision != "fix" || engine.String() != "codex:gpt-5" {
			t.Errorf("arbitrate: %+v with %s", got, engine)
		}
		if read != "package main // as disputed\n" {
			t.Errorf("the arbiter read %q, not the work as it was disputed", read)
		}
	})
}

// m2 renders run d's screen in the popup.
func m2(t *testing.T, d *loopRun) string {
	m, _ := fakeGate(t, []*loopRun{d}, nil)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	return plain(nm.(gateModel))
}

func hasEvent(evs []loopEvent, typ string) bool {
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}
