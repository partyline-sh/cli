package main

import (
	"os/exec"
	"strings"
	"testing"
)

// rowKinds flattens m.rows to a comparable list: "H:<proj>" for a header, "S:<id>" for a session.
func rowKinds(m *aiMenu) []string {
	out := make([]string, 0, len(m.rows))
	for _, r := range m.rows {
		if r.header() && r.group {
			out = append(out, "W:"+r.proj)
		} else if r.header() {
			out = append(out, "H:"+r.proj)
		} else {
			out = append(out, "S:"+m.view[r.sessIdx].ID)
		}
	}
	return out
}

func eqRowKinds(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildRowsTree(t *testing.T) {
	// Two projects: /a has two sessions, /b has one. (Project order follows view order.)
	mk := func() *aiMenu {
		return &aiMenu{view: []aiSession{
			{ID: "s1", Cwd: "/a"}, {ID: "s2", Cwd: "/a"}, {ID: "s3", Cwd: "/b"},
		}}
	}

	// (a) collapsed project shows only its header; (b) expanded shows header+children;
	// (c) project order = view order; (d) single-session project /b still gets a header.
	m := mk()
	m.collapsed = map[string]bool{"/a": true, "/b": false}
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "H:/b", "S:s3"}; !eqRowKinds(got, want) {
		t.Fatalf("collapsed /a + expanded /b: got %v want %v", got, want)
	}

	// default (no overrides, nothing live) → everything collapsed
	m = mk()
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "H:/b"}; !eqRowKinds(got, want) {
		t.Fatalf("default collapsed: got %v want %v", got, want)
	}

	// a live session auto-expands its project (active work shows on open)
	m = mk()
	m.live = map[string]bool{"s1": true}
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "S:s1", "S:s2", "H:/b"}; !eqRowKinds(got, want) {
		t.Fatalf("auto-expand live: got %v want %v", got, want)
	}

	// a search query expands everything regardless of collapse state
	m = mk()
	m.collapsed = map[string]bool{"/a": true, "/b": true}
	m.query = "x"
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "S:s1", "S:s2", "H:/b", "S:s3"}; !eqRowKinds(got, want) {
		t.Fatalf("query expands all: got %v want %v", got, want)
	}

	// (e) header session counts are correct
	m = mk()
	m.collapsed = map[string]bool{"/a": false, "/b": false}
	m.buildRows()
	counts := map[string]int{}
	for _, r := range m.rows {
		if r.header() {
			counts[r.proj] = r.count
		}
	}
	if counts["/a"] != 2 || counts["/b"] != 1 {
		t.Fatalf("header counts: /a=%d (want 2) /b=%d (want 1)", counts["/a"], counts["/b"])
	}
}

// Worktree sessions must NOT become their own top-level projects: they attribute to the
// parent repo and sit in a nested group that starts collapsed.
func TestBuildRowsWorktreeGroup(t *testing.T) {
	mk := func() *aiMenu {
		return &aiMenu{view: []aiSession{
			{ID: "s1", Cwd: "/a", ProjDir: "/a"},
			{ID: "w1", Cwd: "/wt/one", ProjDir: "/a", WtName: "one"},
			{ID: "w2", Cwd: "/wt/two", ProjDir: "/a", WtName: "two"},
		}}
	}

	// Collapsed parent: one row, and its count covers the worktree sessions too (it's the
	// only thing you can see).
	m := mk()
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a"}; !eqRowKinds(got, want) {
		t.Fatalf("collapsed: got %v want %v", got, want)
	}
	if m.rows[0].count != 3 {
		t.Fatalf("parent count = %d, want 3 (worktree sessions included)", m.rows[0].count)
	}

	// Expanded parent: plain sessions, then a collapsed worktrees group header.
	m = mk()
	m.collapsed = map[string]bool{"/a": false}
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "S:s1", "W:/a"}; !eqRowKinds(got, want) {
		t.Fatalf("expanded project, collapsed worktrees: got %v want %v", got, want)
	}
	if m.rows[2].count != 2 {
		t.Fatalf("worktrees group count = %d, want 2", m.rows[2].count)
	}

	// → on the group header expands just the group.
	m.cursor = 2
	m.toggleCollapse()
	if got, want := rowKinds(m), []string{"H:/a", "S:s1", "W:/a", "S:w1", "S:w2"}; !eqRowKinds(got, want) {
		t.Fatalf("expanded worktrees: got %v want %v", got, want)
	}
	if m.cursor != 2 {
		t.Fatalf("cursor = %d after toggling the group, want it to stay on the group header (2)", m.cursor)
	}

	// A live worktree session opens its project AND its group (you can see what's running).
	m = mk()
	m.live = map[string]bool{"w1": true}
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "S:s1", "W:/a", "S:w1", "S:w2"}; !eqRowKinds(got, want) {
		t.Fatalf("live worktree session: got %v want %v", got, want)
	}

	// A search query expands everything, group included.
	m = mk()
	m.query = "x"
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "S:s1", "W:/a", "S:w1", "S:w2"}; !eqRowKinds(got, want) {
		t.Fatalf("query expands all: got %v want %v", got, want)
	}
}

// 'N' spawns a fresh default-engine session with zero questions — the menu route is six
// interactions for the case "just give me claude, here".
func TestQuickNewSpawnsWithoutQuestions(t *testing.T) {
	m := &aiMenu{}
	handled, _ := m.handleKey([]byte{0x4e})
	if handled {
		t.Fatal("N should not need a selected session")
	}
	if !m.quickNew {
		t.Fatal("N should arm the instant spawn")
	}
}

// The engine is env-driven so a codex-first person gets their engine on the same key — but only
// a KNOWN engine; a typo falls back to claude rather than spawning a command that is not there.
func TestQuickNewEngine(t *testing.T) {
	t.Setenv("PARTYLINE_ENGINE", "codex")
	if e := quickNewEngine(); e != "codex" {
		t.Errorf("PARTYLINE_ENGINE should win, got %q", e)
	}
	t.Setenv("PARTYLINE_ENGINE", "clippy")
	if e := quickNewEngine(); e != "claude" {
		t.Errorf("an unknown engine falls back to claude, got %q", e)
	}
	t.Setenv("PARTYLINE_ENGINE", "")
	if e := quickNewEngine(); e != "claude" {
		t.Errorf("unset falls back to claude, got %q", e)
	}
}

// ctrl-n is N with the engine's own skip-permissions mode — a separate chord, not a prompt,
// because a prompt would un-make the point of the key.
func TestCtrlNArmsTheBypassSpawn(t *testing.T) {
	m := &aiMenu{}
	m.handleKey([]byte{0x0e})
	if !m.quickNewBypass || m.quickNew {
		t.Fatalf("ctrl-n arms the bypass spawn only: bypass=%v plain=%v", m.quickNewBypass, m.quickNew)
	}
}

// One definition of "bypass" per engine — the share picker's table — so a new engine added there
// gets the ctrl-n behaviour without a second edit.
func TestBypassFlagsComeFromThePermissionTable(t *testing.T) {
	if got := strings.Join(bypassFlagsFor("claude"), " "); got != "--permission-mode bypassPermissions" {
		t.Errorf("claude bypass = %q", got)
	}
	if got := strings.Join(bypassFlagsFor("codex"), " "); got != "--dangerously-bypass-approvals-and-sandbox" {
		t.Errorf("codex bypass = %q", got)
	}
	if bypassFlagsFor("llm") != nil {
		t.Error("an engine with no permission concept has no bypass")
	}
}

// The flags land at the END of the argv — the operator's explicit word on this launch wins over
// anything the wiring appended.
func TestPermFlagsAppendLast(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	spec, err := newSessionSpec("claude", t.TempDir(), "", "", "", false, true, 0, "--permission-mode", "bypassPermissions")
	if err != nil {
		t.Fatal(err)
	}
	n := len(spec.Argv)
	if n < 2 || spec.Argv[n-2] != "--permission-mode" || spec.Argv[n-1] != "bypassPermissions" {
		t.Errorf("perm flags must be last: %v", spec.Argv)
	}
}
