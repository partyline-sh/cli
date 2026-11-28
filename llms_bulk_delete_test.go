package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bulkSession(t *testing.T, dir, id, cwd string) aiSession {
	t.Helper()
	f := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return aiSession{Tool: "claude", ID: id, Title: id, Cwd: cwd, storePath: f, LastActive: time.Now().Add(-48 * time.Hour)}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// x on a project header confirms once for every closed session under it, moves them only on [y],
// and never touches a running session or another project.
func TestXOnAProjectHeaderDeletesEveryClosedSessionInIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, b := bulkSession(t, home, "a", "/scratch"), bulkSession(t, home, "b", "/scratch")
	live := bulkSession(t, home, "live", "/scratch")
	live.Live = true
	idle := bulkSession(t, home, "idle", "/scratch") // open in tmux but quiet: Live is false
	other := bulkSession(t, home, "other", "/keep")

	m := &aiMenu{view: []aiSession{a, b, live, idle, other}, live: map[string]bool{"idle": true}}
	m.buildRows()
	m.cursor = 0
	if r := m.curRow(); r == nil || !r.header() || r.proj != "/scratch" {
		t.Fatalf("expected the cursor on the /scratch header, got %+v", r)
	}

	m.handleKey([]byte{'x'})
	if m.confirmBulk == nil {
		t.Fatal("x on a header did not open the project confirmation")
	}
	if got := len(m.confirmBulk.sessions); got != 2 || m.confirmBulk.live != 2 {
		t.Fatalf("gathered %d sessions (%d running), want 2 closed and 2 running left alone", got, m.confirmBulk.live)
	}
	body := strings.Join(bulkDeleteModalLines(*m.confirmBulk), "\n")
	for _, want := range []string{"2 session(s) move to the trash", "2 running — left in place", "[y] delete all"} {
		if !strings.Contains(body, want) {
			t.Errorf("confirmation is missing %q:\n%s", want, body)
		}
	}

	m.handleKey([]byte{'n'})
	if m.confirmBulk != nil || !exists(a.storePath) || !exists(b.storePath) {
		t.Fatal("cancel did not leave every file in place")
	}

	m.view = []aiSession{a, b, live, idle, other}
	m.buildRows()
	m.cursor = 0
	m.handleKey([]byte{'x'})
	m.handleKey([]byte{'y'})
	if exists(a.storePath) || exists(b.storePath) {
		t.Error("[y] did not move the project's closed sessions")
	}
	if !exists(live.storePath) || !exists(idle.storePath) {
		t.Error("a running or tmux-open session was moved")
	}
	if !exists(other.storePath) {
		t.Error("a session in another project was moved")
	}
	if !strings.Contains(m.flash, "deleted 2 session(s)") {
		t.Errorf("flash does not report the count: %q", m.flash)
	}
}

// A header whose sessions are all running opens nothing and says why.
func TestXOnAHeaderWithOnlyRunningSessionsExplains(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := bulkSession(t, home, "busy", "/p")
	s.Live = true
	m := &aiMenu{view: []aiSession{s}}
	m.buildRows()
	m.cursor = 0
	m.handleKey([]byte{'x'})
	if m.confirmBulk != nil {
		t.Error("the confirmation opened with nothing to move")
	}
	if !strings.Contains(m.flash, "1 running") {
		t.Errorf("flash does not say why: %q", m.flash)
	}
}

// The nested worktrees header takes only the worktree sessions, not the project's own.
func TestXOnTheWorktreeGroupTakesOnlyWorktreeSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	main := bulkSession(t, home, "main", "/repo")
	wt := bulkSession(t, home, "wt", "/repo")
	wt.WtName = "feature"
	m := &aiMenu{view: []aiSession{main, wt}}
	b := m.bulkFor(treeRow{proj: main.projKey(), sessIdx: -1, group: true})
	if len(b.sessions) != 1 || b.sessions[0].ID != "wt" {
		t.Fatalf("worktree group gathered %+v, want only the worktree session", b.sessions)
	}
}

func TestPruneReason(t *testing.T) {
	home := "/usr" // exists and is not a temp folder
	cases := []struct {
		s    aiSession
		want string
	}{
		{aiSession{Cwd: home, DeadWt: true}, "worktree gone"},
		{aiSession{Cwd: "/tmp/scratch"}, "temp folder"},
		{aiSession{Cwd: "/private/var/folders/x/y"}, "temp folder"},
		{aiSession{Cwd: "/ptln-test-no-such-dir/project"}, ""}, // e.g. another machine's path: kept
		{aiSession{Cwd: "/tmpfoo-ptln-test"}, ""},              // a /tmp prefix is not the temp folder
		{aiSession{Cwd: home}, ""},
		{aiSession{Cwd: ""}, ""},
	}
	for _, c := range cases {
		if got := pruneReason(c.s); got != c.want {
			t.Errorf("pruneReason(%q, dead=%v) = %q, want %q", c.s.Cwd, c.s.DeadWt, got, c.want)
		}
	}
}
