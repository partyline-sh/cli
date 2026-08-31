package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/ptymux"
)

// These tests exist because of a real incident, and they encode both halves of the rule it taught.
//
// A tab must ALWAYS reload the conversation it was created for — otherwise a shared session, a
// label, and `ask_session` by name all stop identifying anything. But pinning alone is exactly
// what stranded a live fork for a day: the human talked to the fork, `--resume` faithfully
// restored the parent, and the last day's work appeared to have vanished.
//
// So the invariant under test is two-sided: the parent is never repointed, AND the fork is never
// lost.

const parentID = "3ab9111a-a373-4c2c-9c2a-9a944a80b880"
const forkID = "db49a7ca-b1fd-4fdc-80b9-b55d65193515"

// Verbatim shape from the roster that produced the incident, trimmed to the fields we read.
func writeRoster(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "daemon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "roster.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func realRoster(pid int) string {
	return `{"proto":1,"workers":{"db49a7ca":{
      "sessionId":"` + forkID + `","cwd":"/Users/darcy/dev/assetmgmt","pid":` + strconv.Itoa(pid) + `,
      "dispatch":{"launch":{"mode":"resume","fork":true,
        "sessionId":"/Users/darcy/.claude/projects/-Users-darcy-dev-assetmgmt/` + parentID + `.jsonl"}}}}}`
}

func parentTab() ptymux.Spec {
	return ptymux.Spec{
		Label:  "Partyline Original",
		Key:    parentID,
		Dir:    "/Users/darcy/dev/assetmgmt",
		Engine: "claude",
		Argv:   []string{"claude", "--resume", parentID, "--permission-mode", "bypassPermissions"},
	}
}

func TestForkIsAdoptedAsItsOwnTab(t *testing.T) {
	writeRoster(t, realRoster(os.Getpid())) // alive
	got := adoptForks([]ptymux.Spec{parentTab()})
	if len(got) != 1 {
		t.Fatalf("expected the fork to be adopted, got %d specs", len(got))
	}
	f := got[0]
	if f.Key != forkID {
		t.Fatalf("adopted tab points at %q, not the fork", f.Key)
	}
	// It must resume the FORK. Inheriting the parent's argv unchanged would reopen the parent under
	// the fork's name — two tabs, one conversation, and the fork still lost.
	if !hasArg(f.Argv, forkID) || hasArg(f.Argv, parentID) {
		t.Fatalf("adopted argv does not target the fork: %v", f.Argv)
	}
	// The posture the human chose has to survive; rebuilding argv from defaults would silently
	// drop it.
	if !hasArg(f.Argv, "bypassPermissions") {
		t.Fatalf("adopted argv dropped the parent's flags: %v", f.Argv)
	}
	if !strings.HasPrefix(f.Label, "Partyline Original") {
		t.Fatalf("adopted label %q does not name its parent", f.Label)
	}
}

// THE load-bearing test. Adoption must only ever ADD.
func TestAdoptionNeverRepointsTheParent(t *testing.T) {
	writeRoster(t, realRoster(os.Getpid()))
	live := []ptymux.Spec{parentTab()}
	saved := workspaceSpecs(live)

	var parent *ptymux.Spec
	for i := range saved {
		if saved[i].Key == parentID {
			parent = &saved[i]
		}
	}
	if parent == nil {
		t.Fatal("the parent tab vanished from the saved workspace")
	}
	if parent.Label != "Partyline Original" {
		t.Fatalf("parent was renamed to %q", parent.Label)
	}
	if !hasArg(parent.Argv, parentID) || hasArg(parent.Argv, forkID) {
		t.Fatalf("parent tab was repointed at the fork: %v", parent.Argv)
	}
	if len(saved) != 2 {
		t.Fatalf("expected parent + fork, got %d", len(saved))
	}
}

// A fork of a session we do NOT have open is somebody else's business. Adopting it would put an
// unrelated conversation on the switchboard.
func TestForkOfAnUnopenedSessionIsIgnored(t *testing.T) {
	writeRoster(t, realRoster(os.Getpid()))
	other := parentTab()
	other.Key = "ffffffff-0000-0000-0000-000000000000"
	if got := adoptForks([]ptymux.Spec{other}); len(got) != 0 {
		t.Fatalf("adopted a fork of a session we do not have open: %v", got)
	}
}

// Already open as its own tab → adopting again would show one conversation twice.
func TestAnAlreadyOpenForkIsNotAdoptedTwice(t *testing.T) {
	writeRoster(t, realRoster(os.Getpid()))
	already := parentTab()
	already.Key = forkID
	if got := adoptForks([]ptymux.Spec{parentTab(), already}); len(got) != 0 {
		t.Fatalf("adopted a fork that is already open: %v", got)
	}
}

// A fork the engine still owns cannot be resumed — the engine refuses a live session. The resume
// path has to be able to SEE that, or it drops the tab silently and reproduces the incident.
func TestHeldForkIsReportedSoItIsNotSilentlyDropped(t *testing.T) {
	writeRoster(t, realRoster(os.Getpid())) // our own pid: definitely alive
	saved := workspaceSpecs([]ptymux.Spec{parentTab()})
	held := heldForks(saved)
	if len(held) != 1 || held[0].SessionID != forkID {
		t.Fatalf("a live fork was not reported as held: %v", held)
	}
}

// Once the engine releases it, it is an ordinary session again and must restore like any other.
func TestAReleasedForkIsNotHeld(t *testing.T) {
	writeRoster(t, realRoster(999999)) // a pid that is not running
	saved := workspaceSpecs([]ptymux.Spec{parentTab()})
	if held := heldForks(saved); len(held) != 0 {
		t.Fatalf("a dead fork was reported as held: %v", held)
	}
}

// Every failure must degrade to today's behaviour. A broken roster breaking `--resume` would be a
// far worse bug than the one being fixed.
func TestABrokenRosterNeverBreaksResume(t *testing.T) {
	for _, body := range []string{"", "{", "null", `{"workers":null}`, `{"workers":{"x":{}}}`} {
		writeRoster(t, body)
		live := []ptymux.Spec{parentTab()}
		saved := workspaceSpecs(live)
		if len(saved) != 1 || saved[0].Key != parentID {
			t.Fatalf("roster %q disturbed the live set: %v", body, saved)
		}
	}
	// No roster file at all — the overwhelmingly common case for anyone not running the daemon.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := adoptForks([]ptymux.Spec{parentTab()}); got != nil {
		t.Fatalf("missing roster produced specs: %v", got)
	}
}

// A non-fork worker (a plain background agent) is not a fork of anything and must not be adopted
// as one — its conversation never belonged to this tab.
func TestANonForkWorkerIsNotAdopted(t *testing.T) {
	writeRoster(t, strings.Replace(realRoster(os.Getpid()), `"fork":true`, `"fork":false`, 1))
	if got := adoptForks([]ptymux.Spec{parentTab()}); len(got) != 0 {
		t.Fatalf("adopted a non-fork worker: %v", got)
	}
}

func hasArg(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
