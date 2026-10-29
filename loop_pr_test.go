package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// prRun saves a finished run in a scratch home and returns it.
func prRun(t *testing.T, state string) *loopRun {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	r := &loopRun{ID: newLoopID(), Repo: t.TempDir(), State: state, Created: time.Now()}
	if err := r.save(loopEvent{Type: state}); err != nil {
		t.Fatal(err)
	}
	return r
}

// fakeGh puts a gh on PATH that prints out and counts its calls in the returned file.
func fakeGh(t *testing.T, out string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + calls + "\ncat <<'JSON'\n" + out + "\nJSON\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func ghCalls(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "\n")
}

func TestParseLoopPR(t *testing.T) {
	for in, want := range map[string]string{
		"1441":  "1441",
		"#1441": "1441",
		"https://github.com/partyline-sh/partyline/pull/1441/": "https://github.com/partyline-sh/partyline/pull/1441",
	} {
		if got, err := parseLoopPR(in); err != nil || got != want {
			t.Errorf("parseLoopPR(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "-3", "abc", "https://github.com/o/r/issues/4", "https://github.com/o/r/pull/x"} {
		if _, err := parseLoopPR(bad); err == nil {
			t.Errorf("parseLoopPR(%q) accepted", bad)
		}
	}
	if n := loopPRNumber("https://github.com/o/r/pull/77"); n != 77 {
		t.Errorf("number of a URL: %d", n)
	}
}

// The PR is saved on the run with an event; a run that has not reached pr_ready refuses one.
func TestRecordLoopPR(t *testing.T) {
	r := prRun(t, loopPRReady)
	if n, err := recordLoopPR(r.ID, "https://github.com/o/r/pull/12"); err != nil || n != 12 {
		t.Fatalf("record: %d, %v", n, err)
	}
	got, _ := loadLoopRun(r.ID)
	evs, _ := loopEvents(r.ID)
	if got.PR != "https://github.com/o/r/pull/12" || evs[len(evs)-1].Type != "pr" {
		t.Errorf("run PR %q, last event %+v", got.PR, evs[len(evs)-1])
	}
	building := &loopRun{ID: newLoopID(), Repo: r.Repo, State: loopBuilding, Created: time.Now()}
	_ = building.save(loopEvent{Type: "approved"})
	if _, err := recordLoopPR(building.ID, "12"); err == nil {
		t.Error("a building run took a PR")
	}
}

// The watcher sums the checks up, caches them, and asks gh again only after prChecksEvery.
func TestRefreshPRChecks(t *testing.T) {
	r := prRun(t, loopPRReady)
	r.PR = "12"
	_ = r.save(loopEvent{Type: "pr"})
	calls := fakeGh(t, `[{"bucket":"pass"},{"bucket":"pending"},{"bucket":"skipping"}]`)
	now := time.Now()
	refreshPRChecks(now)
	if got := cachedPRCheck(r); got != "pending" || ghCalls(calls) != 1 {
		t.Fatalf("state %q after %d gh calls", got, ghCalls(calls))
	}
	refreshPRChecks(now.Add(time.Minute))
	if ghCalls(calls) != 1 {
		t.Errorf("gh asked again within %s", prChecksEvery)
	}
	fakeGh(t, `[{"bucket":"pass"},{"bucket":"fail"}]`)
	refreshPRChecks(now.Add(prChecksEvery))
	if got := cachedPRCheck(r); got != "fail" {
		t.Errorf("after a failed check: %q", got)
	}
}

// No gh, or gh that prints no JSON (signed out), leaves the state unknown.
func TestPRChecksWithoutGh(t *testing.T) {
	r := prRun(t, loopPRReady)
	r.PR = "12"
	_ = r.save(loopEvent{Type: "pr"})
	t.Setenv("PATH", t.TempDir())
	refreshPRChecks(time.Now())
	if got, ok := loadPRChecks()[prChecksKey(r)]; !ok || got.State != "" {
		t.Errorf("no gh: %+v, cached %v", got, ok)
	}
	fakeGh(t, "To get started with GitHub CLI, please run:  gh auth login")
	if got := ghPRChecks(r.Repo, r.PR); got != "" {
		t.Errorf("signed out: %q", got)
	}
}

// The board reads the cache and never runs gh itself; the card shows the number and the checks.
func TestBoardShowsRecordedPR(t *testing.T) {
	r := prRun(t, loopPRReady)
	r.PR = "https://github.com/o/r/pull/42"
	_ = r.save(loopEvent{Type: "pr"})
	savePRChecks(map[string]prCheck{prChecksKey(r): {State: "pass", At: time.Now()}})
	calls := fakeGh(t, `[{"bucket":"fail"}]`)
	rows := tabRows(time.Now())
	if len(rows) != 1 || rows[0].PR != 42 || rows[0].CI != "pass" || ghCalls(calls) != 0 {
		t.Fatalf("rows %+v, gh calls %d", rows, ghCalls(calls))
	}
	st := gateStyle{tmuxTheme()}
	for ci, want := range map[string]string{"pass": "#42 checks✓ · ", "fail": "#42 checks✗ · ", "pending": "#42 checks… · ", "": "#42 · "} {
		row := rows[0]
		row.CI = ci
		if got := gateANSI.ReplaceAllString(tabStateLine(st, row, 60), ""); !strings.HasPrefix(got, want) {
			t.Errorf("CI %q: line %q, want prefix %q", ci, got, want)
		}
	}
}
