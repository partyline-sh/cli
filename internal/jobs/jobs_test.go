package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const good = `
coordination:
  mode: local-only
  tz: America/Edmonton
jobs:
  news:
    engine: codex
    prompt: prompts/news.md
    capabilities: [network, write-worktree]
    limit: { minutes: 20 }
    on: { schedule: "0 7 * * *" }
    catchup: 6h
  solution-record:
    engine: claude
    prompt: prompts/solution.md
    on: { event: loop.accepted }
`

func mustParse(t *testing.T, s string) File {
	t.Helper()
	f, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseAcceptsAGoodFileAndDefaultsTheLimit(t *testing.T) {
	f := mustParse(t, good)
	if f.Jobs["news"].Name != "news" || f.Jobs["news"].Limit.Minutes != 20 {
		t.Errorf("news = %+v", f.Jobs["news"])
	}
	if f.Jobs["solution-record"].Limit.Minutes != DefaultMinutes {
		t.Errorf("default limit not applied: %+v", f.Jobs["solution-record"].Limit)
	}
}

// Every problem is reported at once, and mode is never inferred.
func TestParseReportsEveryProblem(t *testing.T) {
	bad := `
coordination: { tz: Mars/Olympus }
jobs:
  Bad_Name:
    engine: gpt
    prompt: ../outside.md
    capabilities: [root]
    on: { schedule: "every day", event: loop.accepted }
  ok:
    engine: claude
    prompt: p.md
    on: { event: made.up }
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("a bad file parsed")
	}
	for _, want := range []string{"mode is required", "tz", "lowercase", "engine", "inside the repo", "unknown capability", "not both", "made.up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestParseRejectsBadCronAndAcceptsTriggers(t *testing.T) {
	f := strings.Replace(good, `"0 7 * * *"`, `"0 7 * *"`, 1)
	if _, err := Parse([]byte(f)); err == nil || !strings.Contains(err.Error(), "5 fields") {
		t.Errorf("four-field cron accepted: %v", err)
	}
	f = strings.Replace(good, "event: loop.accepted", "event: trigger:deploy-done", 1)
	if _, err := Parse([]byte(f)); err != nil {
		t.Errorf("trigger event rejected: %v", err)
	}
}

func TestRepoIDAgreesAcrossRemoteForms(t *testing.T) {
	want := "github.com/partyline-sh/partyline"
	for _, u := range []string{
		"git@github.com:partyline-sh/partyline.git",
		"https://github.com/partyline-sh/partyline",
		"https://GitHub.com/partyline-sh/partyline.git/",
		"ssh://git@github.com/partyline-sh/partyline.git",
		"ssh://git@github.com:22/partyline-sh/partyline.git",
	} {
		if got, err := RepoID(u); err != nil || got != want {
			t.Errorf("RepoID(%q) = %q, %v", u, got, err)
		}
	}
	if _, err := RepoID("/local/path/repo"); err == nil {
		t.Error("a local path got a repo id")
	}
}

// The hash changes with the prompt, the capabilities and the coordination block, so any of them
// revokes approval; capability order does not.
func TestDefinitionHashCoversWhatChangesBehaviour(t *testing.T) {
	f := mustParse(t, good)
	base, _ := NewDefinition("github.com/o/r", f, "news", []byte("find news"))

	prompt, _ := NewDefinition("github.com/o/r", f, "news", []byte("find other news"))
	if prompt.Hash() == base.Hash() {
		t.Error("a prompt change did not change the hash")
	}

	tz := mustParse(t, strings.Replace(good, "America/Edmonton", "UTC", 1))
	d, _ := NewDefinition("github.com/o/r", tz, "news", []byte("find news"))
	if d.Hash() == base.Hash() {
		t.Error("a tz change did not change the hash")
	}

	swapped := mustParse(t, strings.Replace(good, "[network, write-worktree]", "[write-worktree, network]", 1))
	d, _ = NewDefinition("github.com/o/r", swapped, "news", []byte("find news"))
	if d.Hash() != base.Hash() {
		t.Error("capability order changed the hash")
	}
	if base.ID() != "github.com/o/r#news" || !base.Has(CapNetwork) || base.Has(CapFull) {
		t.Errorf("ID %q, caps %v", base.ID(), base.Job.Capabilities)
	}
}

func TestResultValidation(t *testing.T) {
	ok := Result{Token: "t1", Status: Succeeded, Summary: "6 findings"}
	if err := ok.Validate("t1"); err != nil {
		t.Error(err)
	}
	if err := ok.Validate("t2"); err == nil {
		t.Error("a result for another run was accepted")
	}
	if err := (Result{Token: "t1", Status: Running}).Validate("t1"); err == nil {
		t.Error("a non-final status was accepted")
	}
}

func fixture(t *testing.T) (*FakeRunner, Definition) {
	t.Helper()
	f := mustParse(t, good)
	d, _ := NewDefinition("github.com/o/r", f, "news", []byte("find news"))
	return &FakeRunner{Approvals: &MemApprovals{}, Machine: "macbook"}, d
}

func TestRunnerRefusesUnapprovedAndChangedDefinitions(t *testing.T) {
	r, d := fixture(t)
	if _, err := r.Start(context.Background(), d, StartOptions{}); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("unapproved start: %v", err)
	}
	_ = r.Approvals.Approve(d)
	changed, _ := NewDefinition(d.RepoID, mustParse(t, good), "news", []byte("edited prompt"))
	if _, err := r.Start(context.Background(), changed, StartOptions{}); !errors.Is(err, ErrNotApproved) {
		t.Errorf("an edited prompt ran on the old approval: %v", err)
	}
}

// One active run per job; a verified stop frees it; an unverified stop holds the lock until Accept.
func TestRunnerLockAndUnverifiedStop(t *testing.T) {
	r, d := fixture(t)
	_ = r.Approvals.Approve(d)
	ctx := context.Background()
	first, err := r.Start(ctx, d, StartOptions{Slot: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(ctx, d, StartOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second run of the same job started: %v", err)
	}
	stopped, _ := r.Stop(ctx, first.ID)
	if stopped.Label() != "stopped" {
		t.Errorf("verified stop label %q", stopped.Label())
	}

	r.Leftover = []Process{{PID: 4242, Command: "sleep 600"}}
	second, err := r.Start(ctx, d, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	unverified, _ := r.Stop(ctx, second.ID)
	if unverified.Label() != "stopped (unverified)" || len(unverified.Leftover) != 1 {
		t.Errorf("unverified stop = %q, %+v", unverified.Label(), unverified.Leftover)
	}
	if _, err := r.Start(ctx, d, StartOptions{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a new run started while an unverified stop holds the lock: %v", err)
	}
	if _, err := r.Accept(second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(ctx, d, StartOptions{}); err != nil {
		t.Errorf("Accept did not release the lock: %v", err)
	}
}

func TestFinishRequiresAValidResult(t *testing.T) {
	r, d := fixture(t)
	_ = r.Approvals.Approve(d)
	run, _ := r.Start(context.Background(), d, StartOptions{})
	done, _ := r.Finish(run.ID, "tok", Result{Token: "wrong", Status: Succeeded})
	if done.State != Failed || !strings.Contains(done.Reason, "no valid result") {
		t.Errorf("a bad token finished as %s (%s)", done.State, done.Reason)
	}
}

// History survives a new Store on the same directory (a restart), newest first, and a damaged
// record does not hide the others.
func TestStoreKeepsHistory(t *testing.T) {
	root := t.TempDir()
	s := Store{Root: root}
	job := "github.com/o/r#news"
	t0 := time.Date(2026, 11, 28, 14, 0, 0, 0, time.UTC)
	for i, st := range []State{Succeeded, Missed, Failed} {
		run := Run{ID: "run-" + string(rune('a'+i)), JobID: job, State: st, Started: t0.Add(time.Duration(i) * time.Hour)}
		if err := s.Prepare(&run); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(run); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(s.Dir(job, "run-a"), "run.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err := Store{Root: root}.List(job)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].State != Failed || runs[1].State != Missed {
		t.Fatalf("history = %+v", runs)
	}
	if runs[0].LogPath == "" || !strings.HasSuffix(runs[0].ResultDir, "result") {
		t.Errorf("log/result paths missing: %+v", runs[0])
	}
	if _, err := s.Get(job, "nope"); !errors.Is(err, ErrNoRun) {
		t.Errorf("missing run: %v", err)
	}
}

func TestReadResultFromTheRunDirectory(t *testing.T) {
	s := Store{Root: t.TempDir()}
	run := Run{ID: "run-x", JobID: "github.com/o/r#news"}
	if err := s.Prepare(&run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.ResultDir, "result.json"), []byte(`{"token":"t","status":"succeeded","summary":"ok","artifacts":["briefing.md"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ReadResult(run, "t")
	if err != nil || res.Summary != "ok" || len(res.Artifacts) != 1 {
		t.Errorf("ReadResult = %+v, %v", res, err)
	}
	if _, err := ReadResult(run, "other"); err == nil {
		t.Error("a result for another run validated")
	}
}

func TestParseIsStrictAboutFieldNames(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(good, "capabilities:", "capabilties:", 1)))
	if err == nil || !strings.Contains(err.Error(), "capabilties") {
		t.Errorf("a misspelled field was accepted: %v", err)
	}
}

func TestCronBoundsAndSteps(t *testing.T) {
	for _, bad := range []string{"99 99 * * *", "*/0 * * * *", "0 7 * * 8", "30-10 * * * *", "0 7 0 * *", "a * * * *"} {
		if err := checkCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"0 7 * * *", "*/15 9-17 * * 1-5", "0 0 1,15 * *", "0 8 * * 7"} {
		if err := checkCron(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
}

func TestStoreRejectsEscapingRunIDs(t *testing.T) {
	s := Store{Root: t.TempDir()}
	for _, id := range []string{"..", "../x", "a/b", "", ".hidden"} {
		r := Run{ID: id, JobID: "github.com/o/r#news"}
		if err := s.Prepare(&r); err == nil {
			t.Errorf("Prepare accepted run id %q", id)
		}
		if err := s.Save(r); err == nil {
			t.Errorf("Save accepted run id %q", id)
		}
		if _, err := s.Get(r.JobID, id); !errors.Is(err, ErrNoRun) {
			t.Errorf("Get(%q) = %v", id, err)
		}
	}
}

// A valid result arriving after an unverified stop is kept, but does not end the uncertainty or
// release the job's lock.
func TestLateResultAfterUnverifiedStopKeepsTheLock(t *testing.T) {
	r, d := fixture(t)
	_ = r.Approvals.Approve(d)
	ctx := context.Background()
	r.Leftover = []Process{{PID: 7, Command: "agent"}}
	run, _ := r.Start(ctx, d, StartOptions{})
	_, _ = r.Stop(ctx, run.ID)
	late, _ := r.Finish(run.ID, "tok", Result{Token: "tok", Status: Succeeded, Summary: "done anyway"})
	if late.State != Stopped || late.Label() != "stopped (unverified)" || late.Result == nil {
		t.Errorf("late result changed the run: %s %+v", late.Label(), late.Result)
	}
	if _, err := r.Start(ctx, d, StartOptions{}); !errors.Is(err, ErrBusy) {
		t.Errorf("a late result released the lock: %v", err)
	}
}
