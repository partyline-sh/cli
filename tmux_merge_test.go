package main

import (
	"os/exec"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/ptymux"
)

// tmux_merge_test.go — merging two live sessions into one window, and getting that split back
// from `--resume`.
//
// The reason these exist: before merging, a session WAS a window, and the Spec that identified
// it hung off the window. Merging moves a pane between windows, so anything still keyed to the
// window either lost the session's identity or found its neighbour's. These tests pin the
// pane-shaped model against a real tmux server, because none of it can be checked by reasoning
// about the option hierarchy — it has to be observed.

// tmuxTestServer stands up an isolated server and returns nothing; the socket env does the work.
func tmuxTestServer(t *testing.T, socket string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", socket)
	// stateDir() follows HOME: keep tmuxSaveWorkspace off the developer's real workspace file.
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	_ = tmuxCmd("kill-server").Run()
}

// openTagged opens one session as its own window and returns its pane.
func openTagged(t *testing.T, sp ptymux.Spec) string {
	t.Helper()
	var args []string
	if tmuxCmd("has-session", "-t", tmuxSessionName).Run() != nil {
		args = []string{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "-n", tmuxWindowName(sp.Label), "--", "sleep", "300"}
	} else {
		args = []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmuxSessionName, "-n", tmuxWindowName(sp.Label), "--", "sleep", "300"}
	}
	out, err := tmuxCmd(args...).CombinedOutput()
	if err != nil {
		t.Fatalf("open %q: %v\n%s", sp.Label, err, out)
	}
	pane := strings.TrimSpace(string(out))
	tagTmuxPane(pane, sp)
	return pane
}

func windowOf(t *testing.T, pane string) string {
	t.Helper()
	out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_id}").Output()
	if err != nil {
		t.Fatalf("window of %s: %v", pane, err)
	}
	return strings.TrimSpace(string(out))
}

// A pane keeps its Spec when it is moved into another window. This is the whole reason the tag
// moved off the window: a window option stays with the window the session LEFT.
func TestMergedPaneKeepsItsSpec(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge1")
	a := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	b := openTagged(t, ptymux.Spec{Label: "codex · billing", Key: "kb", Argv: []string{"sleep", "300"}})

	if out, err := tmuxCmd("join-pane", "-h", "-s", b, "-t", windowOf(t, a)).CombinedOutput(); err != nil {
		t.Fatalf("join-pane: %v\n%s", err, out)
	}

	sp, ok := paneSpec(b)
	if !ok {
		t.Fatal("the moved session lost its spec — it is no longer addressable at all")
	}
	if sp.Key != "kb" || sp.Label != "codex · billing" {
		t.Fatalf("the moved pane is answering with its neighbour's identity: %+v", sp)
	}
	if sp, _ := paneSpec(a); sp.Key != "ka" {
		t.Fatalf("the destination pane's own spec changed: %+v", sp)
	}
}

// Two sessions sharing a window come back from a save as one group, with the layout that
// decides the pane sizes.
func TestWorkspaceSpecsGroupMergedSessions(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge2")
	a := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	b := openTagged(t, ptymux.Spec{Label: "codex · billing", Key: "kb", Argv: []string{"sleep", "300"}})
	lone := openTagged(t, ptymux.Spec{Label: "gemini · docs", Key: "kc", Argv: []string{"sleep", "300"}})
	_ = lone
	if out, err := tmuxCmd("join-pane", "-h", "-s", b, "-t", windowOf(t, a)).CombinedOutput(); err != nil {
		t.Fatalf("join-pane: %v\n%s", err, out)
	}

	specs := tmuxWorkspaceSpecs()
	byKey := map[string]ptymux.Spec{}
	for _, sp := range specs {
		byKey[sp.Key] = sp
	}
	if len(byKey) != 3 {
		t.Fatalf("want all three sessions saved, got %d: %+v", len(byKey), specs)
	}
	if byKey["ka"].Group == "" || byKey["ka"].Group != byKey["kb"].Group {
		t.Fatalf("merged sessions must share a group: ka=%q kb=%q", byKey["ka"].Group, byKey["kb"].Group)
	}
	// Ungrouped is what every workspace file written before merging existed looks like, and what
	// a session in a window of its own must keep looking like.
	if byKey["kc"].Group != "" || byKey["kc"].Layout != "" {
		t.Fatalf("a session alone in its window must stay ungrouped: %+v", byKey["kc"])
	}
	if byKey["ka"].Layout == "" {
		t.Fatal("the group's first session must carry the layout, or resume guesses the sizes")
	}
	if byKey["kb"].Layout != "" {
		t.Errorf("only the first session of a group carries the layout, got %q", byKey["kb"].Layout)
	}
}

// The round trip the user actually asked for: merge, save, and have `--resume` put the split
// back rather than scattering the sessions into a window each.
func TestMergedWorkspaceResumesAsASplit(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge3")
	a := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	b := openTagged(t, ptymux.Spec{Label: "codex · billing", Key: "kb", Argv: []string{"sleep", "300"}})
	openTagged(t, ptymux.Spec{Label: "gemini · docs", Key: "kc", Argv: []string{"sleep", "300"}})
	if out, err := tmuxCmd("join-pane", "-h", "-s", b, "-t", windowOf(t, a)).CombinedOutput(); err != nil {
		t.Fatalf("join-pane: %v\n%s", err, out)
	}
	saved := tmuxWorkspaceSpecs()

	// a fresh server, as a resume on a new day finds it
	_ = tmuxCmd("kill-server").Run()
	if _, err := tmuxMerge(saved); err != nil {
		t.Fatalf("resume: %v", err)
	}

	out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F", "#{window_id}\t#{@ptln_key}").Output()
	if err != nil {
		t.Fatal(err)
	}
	winOf := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if w, k, ok := strings.Cut(line, "\t"); ok && k != "" {
			winOf[k] = w
		}
	}
	if len(winOf) != 3 {
		t.Fatalf("resume opened %d sessions, want 3: %v", len(winOf), winOf)
	}
	if winOf["ka"] != winOf["kb"] {
		t.Error("the merged pair came back in separate windows — the split was not remembered")
	}
	if winOf["kc"] == winOf["ka"] {
		t.Error("an unmerged session was swept into the split")
	}
}

// Restoring must not duplicate a group that is already open — the same dedupe rule tmuxMerge has
// always applied to windows, now applied to a session inside a shared one.
func TestResumingAnOpenSplitDoesNotDuplicateIt(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge4")
	a := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	b := openTagged(t, ptymux.Spec{Label: "codex · billing", Key: "kb", Argv: []string{"sleep", "300"}})
	if out, err := tmuxCmd("join-pane", "-h", "-s", b, "-t", windowOf(t, a)).CombinedOutput(); err != nil {
		t.Fatalf("join-pane: %v\n%s", err, out)
	}
	saved := tmuxWorkspaceSpecs()

	if _, err := tmuxMerge(saved); err != nil {
		t.Fatalf("re-merge: %v", err)
	}
	if n := tmuxPaneCount(a); n != 2 {
		t.Fatalf("re-opening an already-open split gave the window %d panes, want 2", n)
	}
}

// A session merged in beside another is still reachable by key — the path a peer answer takes.
// A window-shaped lookup would deliver it to whichever pane the human last clicked.
func TestPaneForKeyFindsAMergedSession(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge5")
	a := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	b := openTagged(t, ptymux.Spec{Label: "codex · billing", Key: "kb", Argv: []string{"sleep", "300"}})
	if out, err := tmuxCmd("join-pane", "-h", "-s", b, "-t", windowOf(t, a)).CombinedOutput(); err != nil {
		t.Fatalf("join-pane: %v\n%s", err, out)
	}
	// the human is looking at the pane they merged INTO
	_ = tmuxCmd("select-pane", "-t", a).Run()

	pane, label, ok := paneForKey("kb")
	if !ok {
		t.Fatal("a merged session is unreachable by key")
	}
	if pane != b {
		t.Fatalf("paneForKey returned %s, want the merged session's own pane %s", pane, b)
	}
	if label != "codex · billing" {
		t.Errorf("label = %q — a merged session is being named by the window it shares", label)
	}

	live := tmuxTargets{}.LiveSessions()
	keys := map[string]string{}
	for _, s := range live {
		keys[s.Key] = s.Label
	}
	if keys["ka"] == "" || keys["kb"] == "" {
		t.Fatalf("both merged sessions must be listed as live, got %+v", live)
	}
	if keys["ka"] == keys["kb"] {
		t.Errorf("merged sessions are listed under one shared name (%q) — they cannot be told apart", keys["ka"])
	}
}

// Breaking a merged session out again. Merging is only safe to offer if it is reversible.
func TestBreakingASessionBackOut(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge6")
	a := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	b := openTagged(t, ptymux.Spec{Label: "codex · billing", Key: "kb", Argv: []string{"sleep", "300"}})
	if out, err := tmuxCmd("join-pane", "-h", "-s", b, "-t", windowOf(t, a)).CombinedOutput(); err != nil {
		t.Fatalf("join-pane: %v\n%s", err, out)
	}
	_ = tmuxFocus(b)

	if err := tmuxRearrange(tmuxMenuItem{brk: true}, a); err != nil {
		t.Fatalf("break out: %v", err)
	}
	if windowOf(t, b) == windowOf(t, a) {
		t.Fatal("the session did not move to a window of its own")
	}
	if sp, ok := paneSpec(b); !ok || sp.Key != "kb" {
		t.Fatalf("the broken-out session lost its identity: %+v ok=%v", sp, ok)
	}
	specs := tmuxWorkspaceSpecs()
	for _, sp := range specs {
		if sp.Group != "" {
			t.Errorf("nothing is merged any more, so nothing may still be grouped: %+v", sp)
		}
	}
}

// The menu's merge action end to end: highlight one session, and it moves in beside the one you
// opened the menu from.
func TestRearrangeMergesTheHighlightedSession(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge7")
	origin := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	other := openTagged(t, ptymux.Spec{Label: "codex · billing", Key: "kb", Argv: []string{"sleep", "300"}})
	// arrowing onto a row previews it, which is what makes it the active pane
	_ = tmuxFocus(other)

	if err := tmuxRearrange(tmuxMenuItem{merge: true}, origin); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if windowOf(t, other) != windowOf(t, origin) {
		t.Fatal("the highlighted session did not come over")
	}
	if tmuxPaneCount(origin) != 2 {
		t.Fatalf("want two sessions in the window, got %d", tmuxPaneCount(origin))
	}
}

// Merging a session into the window it is already in is a no-op, not an error and not a
// self-join: pressing + without arrowing anywhere is the easiest mistake to make.
func TestRearrangeRefusesToMergeASessionWithItself(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge8")
	origin := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	_ = tmuxFocus(origin)

	if err := tmuxRearrange(tmuxMenuItem{merge: true}, origin); err != nil {
		t.Fatalf("want a quiet refusal, got %v", err)
	}
	if n := tmuxPaneCount(origin); n != 1 {
		t.Fatalf("the window gained a pane from merging with itself: %d", n)
	}
}

// The launcher is a fixture with a window of its own; merging it away would leave no way back
// to the session browser.
func TestRearrangeRefusesToMergeTheLauncher(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge9")
	origin := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	launcher := openTagged(t, ptymux.Spec{Label: "⌂ launcher", Key: tmuxLauncherKey, Argv: []string{"sleep", "300"}})
	// the launcher predates per-pane tagging, so it is tagged the way the fixture really is
	tagTmuxWindow(windowOf(t, launcher), ptymux.Spec{Label: "⌂ launcher", Key: tmuxLauncherKey, Argv: []string{"sleep", "300"}})
	_ = tmuxFocus(launcher)

	if err := tmuxRearrange(tmuxMenuItem{merge: true}, origin); err != nil {
		t.Fatalf("want a quiet refusal, got %v", err)
	}
	if windowOf(t, launcher) == windowOf(t, origin) {
		t.Fatal("the launcher was merged away")
	}
}

// A session joined in on the LEFT sits at pane index 1 but screen position 0. Saving by index
// would restore the pair with the sessions on opposite sides from where they were left — the
// split comes back, but not the one the human arranged.
func TestWorkspaceSavesPanesInScreenOrder(t *testing.T) {
	tmuxTestServer(t, "ptln-test-merge10")
	a := openTagged(t, ptymux.Spec{Label: "claude · payments", Key: "ka", Argv: []string{"sleep", "300"}})
	b := openTagged(t, ptymux.Spec{Label: "codex · billing", Key: "kb", Argv: []string{"sleep", "300"}})
	tail := openTagged(t, ptymux.Spec{Label: "gemini · docs", Key: "kc", Argv: []string{"sleep", "300"}})
	_ = tail
	// -b puts it to the LEFT of ka, so screen order is kb, ka while index order stays ka, kb
	if out, err := tmuxCmd("join-pane", "-b", "-h", "-s", b, "-t", windowOf(t, a)).CombinedOutput(); err != nil {
		t.Fatalf("join-pane: %v\n%s", err, out)
	}

	specs := tmuxWorkspaceSpecs()
	var order []string
	for _, sp := range specs {
		order = append(order, sp.Key)
	}
	if len(order) != 3 || order[0] != "kb" || order[1] != "ka" {
		t.Fatalf("panes saved in %v, want the leftmost session first: kb ka", order)
	}
	// …and the window that was never touched still comes after the one holding the split.
	if order[2] != "kc" {
		t.Fatalf("window order changed: %v", order)
	}
	if specs[0].Layout == "" {
		t.Error("the leftmost session of a group must be the one carrying the layout")
	}
}
