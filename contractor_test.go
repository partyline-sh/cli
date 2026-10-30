package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestContractors(t *testing.T) {
	t.Run("row", testContractorRow)
	t.Run("seen", testContractorSeen)
	t.Run("reconcile", testContractorReconcile)
	t.Run("scans", testContractorScans)
	t.Run("badges", testContractorBadges)
	t.Run("starting", testContractorStarting)
	t.Run("runner", testContractorRunner)
	t.Run("ids", testContractorIDs)
	t.Run("open fails", testContractorOpenFails)
	t.Run("prefix", testContractorSessionPrefix)
	t.Run("gate", testContractorGate)
}

func fakeContractor(t *testing.T, c contractor) *contractor {
	t.Helper()
	if c.Started.IsZero() {
		c.Started = time.Now().Add(-4 * time.Minute)
	}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	return &c
}

// The row groups chips under their parent's tab, a spinner for a running one, ✓/✗ for a finished
// one not yet seen; seen ones are not on it.
func testContractorRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeContractor(t, contractor{ID: "aa0001", Parent: "pane:%1", Role: "builder", Engine: "codex:gpt", Title: "build step 3", State: "running"})
	fakeContractor(t, contractor{ID: "bb0002", Parent: "pane:%1", Role: "reviewer", Engine: "claude:opus", Title: "review r2", State: "done", Ended: time.Now()})
	fakeContractor(t, contractor{ID: "cc0003", Parent: "pane:%9", Role: "builder", Engine: "codex:gpt", Title: "tests", State: "failed", Ended: time.Now()})
	fakeContractor(t, contractor{ID: "dd0004", Parent: "pane:%9", Role: "builder", Engine: "codex:gpt", Title: "old", State: "done", Seen: true})
	names := map[string]string{"pane:%1": "12 Partyline Original", "pane:%9": "9 Kikkr"}
	row := contractorRow(loadContractors(), func(p string) string { return names[p] }, time.Now())
	plain := tmuxFormat.ReplaceAllString(row, "")
	for _, want := range []string{"12 Partyline Original: ", "codex · build step 3", "✓ claude · review r2", "9 Kikkr: ", "✗ codex · tests"} {
		if !strings.Contains(plain, want) {
			t.Errorf("row lacks %q:\n%s", want, plain)
		}
	}
	for _, want := range []string{"#[range=user|aa0001]", "#[range=user|cc0003]"} {
		if !strings.Contains(row, want) {
			t.Errorf("row lacks the click range %q", want)
		}
	}
	if strings.Contains(plain, "· old") {
		t.Error("a seen contractor is still on the row")
	}
	if strings.Index(plain, "build step 3") > strings.Index(plain, "9 Kikkr") {
		t.Error("chips are not grouped under their parent")
	}
	if strings.Contains(contractorRow([]*contractor{{ID: "x", Parent: "p", Title: "a#[fg=red]b", State: "running"}}, func(string) string { return "p" }, time.Now()), "a#[fg=red]") {
		t.Error("a title can inject tmux format")
	}
}

// A finished chip stays until opened or its parent's tab is selected.
func testContractorSeen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubContractorTmux(t)
	fakeContractor(t, contractor{ID: "ee0005", Parent: "pane:%3", State: "done", Ended: time.Now()})
	fakeContractor(t, contractor{ID: "ff0006", Parent: "pane:%4", State: "done", Ended: time.Now()})
	contractorsSeen("ee0005", "")
	if c, _ := loadContractor("ee0005"); !c.Seen {
		t.Error("an opened chip was not marked seen")
	}
	if c, _ := loadContractor("ff0006"); c.Seen {
		t.Error("another chip was marked seen")
	}
}

// Reconcile: a running record whose window is gone has failed; the badge counts running ones per
// parent; the session goes with its last contractor; the row shows while anything is on it.
func testContractorReconcile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubContractorTmux(t)
	fakeContractor(t, contractor{ID: "ab0007", Parent: "pane:%5", State: "running", Window: "@gone"})
	contractorsReconcile()
	if c, _ := loadContractor("ab0007"); c.State != "failed" || !strings.Contains(c.Result, "window closed") {
		t.Errorf("a dead running record: %+v", c)
	}
	if !calls.rowShown {
		t.Error("an unseen failure did not keep the row")
	}
	if calls.badges["%5"] != 0 {
		t.Error("a dead contractor still counted in the badge")
	}
	contractorsSeen("ab0007", "")
	if calls.rowShown {
		t.Error("the row stayed after the last chip was seen")
	}
}

// The indicator scan reads the person's session only: on a scratch server, it leaves a window in
// the contractor session alone while a run is waiting.
func testContractorScans(t *testing.T) {
	tmuxTestServer(t, "ptln-test-contractor-scan")
	if err := tmuxCmd("new-session", "-d", "-s", tmuxSessionName, "sleep", "60").Run(); err != nil {
		t.Fatal(err)
	}
	win, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{window_id}", "-s", contractorSession, "sleep", "60").Output()
	if err != nil {
		t.Fatal(err)
	}
	// A marker the scan would clear if it visited the window: it must still be there after.
	cw := strings.TrimSpace(string(win))
	if err := tmuxCmd("set-option", "-w", "-t", cw, "@ptln_needs", "marker").Run(); err != nil {
		t.Fatal(err)
	}
	waitingRun(t, loopAwaiting)
	loopMarkWaiting(nil)
	if out, _ := tmuxCmd("show-options", "-wv", "-t", cw, "@ptln_needs").Output(); strings.TrimSpace(string(out)) != "marker" {
		t.Errorf("the scan visited the contractor session's window (@ptln_needs now %q)", strings.TrimSpace(string(out)))
	}
}

// tmuxFormat matches tmux's #[...] style and range directives.
var tmuxFormat = regexp.MustCompile(`#\[[^\]]*\]`)

type contractorTmuxCalls struct {
	rowShown bool
	badges   map[string]int
}

// stubContractorTmux replaces the row and badge setters and makes every window but "@live" gone.
func stubContractorTmux(t *testing.T) *contractorTmuxCalls {
	t.Helper()
	calls := &contractorTmuxCalls{}
	oldRow, oldBadges, oldAlive, oldActive := setContractorRow, setContractorBadges, contractorAlive, contractorRowActive
	t.Cleanup(func() {
		setContractorRow, setContractorBadges, contractorAlive, contractorRowActive = oldRow, oldBadges, oldAlive, oldActive
	})
	setContractorRow = func(show bool) { calls.rowShown = show }
	contractorRowActive = func() bool { return calls.rowShown }
	setContractorBadges = func(b map[string]int) { calls.badges = b }
	contractorAlive = func(c *contractor) bool { return c.Window == "@live" }
	return calls
}

// The badge counts each parent's running contractors.
func testContractorBadges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubContractorTmux(t)
	fakeContractor(t, contractor{ID: "ac0001", Parent: "pane:%5", State: "running", Window: "@live"})
	fakeContractor(t, contractor{ID: "ac0002", Parent: "pane:%5", State: "running", Window: "@live"})
	fakeContractor(t, contractor{ID: "ac0003", Parent: "pane:%6", State: "running", Window: "@live"})
	fakeContractor(t, contractor{ID: "ac0004", Parent: "pane:%6", State: "done", Ended: time.Now()})
	contractorsReconcile()
	if calls.badges["%5"] != 2 || calls.badges["%6"] != 1 {
		t.Errorf("badges %v, want %%5:2 %%6:1", calls.badges)
	}
}

// A start holds the records lock until its window is recorded, so a "starting" record a reconcile
// sees was abandoned: it fails, the loop is told, it shows on the row until seen, and it does not
// keep the session alive. A runner whose record never leaves starting fails it too.
func testContractorStarting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubContractorTmux(t)
	killed := false
	old := killContractorSession
	t.Cleanup(func() { killContractorSession = old })
	killContractorSession = func(live int) { killed = live == 0 }
	r := testLoopRun(t, t.TempDir())
	fakeContractor(t, contractor{ID: "bd0001", Parent: "pane:%5", Loop: r.ID, State: "starting"})
	contractorsReconcile()
	if c, _ := loadContractor("bd0001"); c.State != "failed" || c.Result != "it was never started" {
		t.Errorf("an abandoned start: %+v", c)
	}
	if !killed {
		t.Error("an abandoned start kept the session")
	}
	if !calls.rowShown {
		t.Error("an abandoned start is not on the row")
	}
	if msgs := loopTake(r.ID); !strings.Contains(strings.Join(msgs, "\n"), "contractor bd0001") {
		t.Errorf("the loop was not told: %v", msgs)
	}
	contractorsSeen("bd0001", "")
	if calls.rowShown {
		t.Error("a seen abandoned start stayed on the row")
	}

	oldWait := contractorStartWait
	t.Cleanup(func() { contractorStartWait = oldWait })
	contractorStartWait = 200 * time.Millisecond
	fakeContractor(t, contractor{ID: "bd0002", Parent: "pane:%5", Loop: r.ID, State: "starting", Argv: []string{"true"}})
	contractorMain("bd0002")
	if c, _ := loadContractor("bd0002"); c.State != "failed" || c.Result != "its window was never recorded" {
		t.Errorf("a runner that timed out: %+v", c)
	}
	if msgs := loopTake(r.ID); !strings.Contains(strings.Join(msgs, "\n"), "contractor bd0002") {
		t.Errorf("the loop was not told of the timeout: %v", msgs)
	}
}

// The runner waits for its window to be recorded, runs, keeps a bounded last line however long the
// output's lines, and tells the loop how it ended.
func testContractorRunner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubContractorTmux(t)
	old := killContractorSession
	t.Cleanup(func() { killContractorSession = old })
	killContractorSession = func(int) {}
	r := testLoopRun(t, t.TempDir())
	fakeContractor(t, contractor{ID: "ce0001", Parent: "pane:%5", Loop: r.ID, Role: "helper", Engine: "stub:sh", State: "starting", Dir: t.TempDir(),
		// One 200 KB line, made in the shell: Linux refuses a single argument over 128 KB.
		Argv: []string{"sh", "-c", "head -c 204800 /dev/zero | tr '\\0' x; echo; echo finished"}})
	done := make(chan struct{})
	go func() { contractorMain("ce0001"); close(done) }()
	time.Sleep(200 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("the runner did not wait for its window to be recorded")
	default:
	}
	_ = updateContractor("ce0001", func(c *contractor) bool { c.State, c.Window = "running", "@live"; return true })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the runner did not finish (a long line blocked it?)")
	}
	c, _ := loadContractor("ce0001")
	if c.State != "done" || c.Result != "finished" {
		t.Errorf("ended %s with %q", c.State, c.Result)
	}
	if msgs := loopTake(r.ID); len(msgs) == 0 || !strings.Contains(strings.Join(msgs, "\n"), "contractor ce0001") {
		t.Errorf("the loop was not told: %v", msgs)
	}
}

// Ids: a generated one is 14 hex (a tmux range name holds 15); starting an id that exists is refused and leaves the record; a
// record whose id is not a contractor id, or not its file's name, is not loaded.
func testContractorIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubContractorTmux(t)
	orig := fakeContractor(t, contractor{ID: "dd1dd1", Parent: "pane:%5", Title: "first", State: "running", Window: "@live"})
	if err := startContractor(&contractor{ID: "dd1dd1", Parent: "pane:%6", Title: "second"}); err == nil {
		t.Error("a duplicate id was started")
	}
	if c, _ := loadContractor(orig.ID); c.Title != "first" || c.State != "running" {
		t.Errorf("the duplicate start changed the original: %+v", c)
	}
	for file, id := range map[string]string{"x;touch-p": "x;touch-p", "ff1ff1": "ee1ee1"} { // a bad id; another id's file
		b, _ := json.Marshal(contractor{ID: id, State: "done"})
		_ = os.WriteFile(contractorPath(file), b, 0o600)
	}
	for _, c := range loadContractors() {
		if c.ID != "dd1dd1" {
			t.Errorf("loaded a record with id %q", c.ID)
		}
	}
}

// A window that cannot be opened fails the record and tells the loop. A socket name past the
// socket path limit makes every tmux call fail without starting a server.
func testContractorOpenFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PARTYLINE_TMUX_SOCKET", strings.Repeat("x", 200))
	stubContractorTmux(t)
	r := testLoopRun(t, t.TempDir())
	c := &contractor{Parent: "pane:%5", Loop: r.ID, Role: "helper", Engine: "stub:sh", Dir: t.TempDir(), Argv: []string{"true"}}
	if err := startContractor(c); err == nil {
		t.Fatal("startContractor succeeded with no tmux")
	}
	if !regexp.MustCompile(`^[0-9a-f]{14}$`).MatchString(c.ID) {
		t.Errorf("generated id %q is not 14 hex", c.ID)
	}
	if got, _ := loadContractor(c.ID); got.State != "failed" || !strings.Contains(got.Result, "could not open its window") {
		t.Errorf("the record: %+v", got)
	}
	if msgs := loopTake(r.ID); !strings.Contains(strings.Join(msgs, "\n"), "contractor "+c.ID) {
		t.Errorf("the loop was not told: %v", msgs)
	}
}

// With only the contractor session left, a `-t ptln` target must not land in it.
func testContractorSessionPrefix(t *testing.T) {
	tmuxTestServer(t, "ptln-test-contractor-prefix")
	if err := tmuxCmd("new-session", "-d", "-s", contractorSession, "sleep", "60").Run(); err != nil {
		t.Fatal(err)
	}
	if tmuxCmd("has-session", "-t", tmuxSessionName).Run() == nil {
		t.Errorf("has-session -t %s matched the %s session", tmuxSessionName, contractorSession)
	}
}

// A process under a contractor session's pane fails the approval popup's check.
func testContractorGate(t *testing.T) {
	tmuxTestServer(t, "ptln-test-contractor-gate")
	if err := tmuxCmd("new-session", "-d", "-s", tmuxSessionName, "sleep", "60").Run(); err != nil {
		t.Fatal(err)
	}
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_pid}", "-s", contractorSession, "sleep", "60").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	_, pids, err := gatePanes()
	if err != nil {
		t.Fatal(err)
	}
	if gateNotUnderAPane([]int{os.Getpid(), pid}, pids) == nil {
		t.Error("a process under a contractor pane passed the check")
	}
}
