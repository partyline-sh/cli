package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The load-bearing parts are the ones that decide what gets SCANNED and what environment a session
// RESUMES with. Both fail quietly if they are wrong: a missed root looks like "no sessions", and a
// missed HOME looks like "that session doesn't exist" — reported by the engine, so it reads as the
// engine's fault rather than ours.

func withHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
}

func TestPrimaryRootIsAlwaysFirstAndAlwaysPresent(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)

	roots := loadSessionRoots()
	if len(roots) == 0 || roots[0].Home != home || !roots[0].Primary {
		t.Fatalf("roots = %+v, want the process's own home first and marked primary", roots)
	}
}

// A corrupt or empty adopted-roots file must degrade to today's behaviour — scanning your own home
// — and never to "no sessions at all". A discovery feature that can break the base case is worse
// than no discovery feature.
func TestACorruptRootsFileDegradesToTheOwnHome(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	if err := os.MkdirAll(daemonDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir(), rootsFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	roots := loadSessionRoots()
	if len(roots) != 1 || roots[0].Home != home {
		t.Errorf("roots = %+v, want just the own home", roots)
	}
}

func TestAdoptingIsIdempotentAndRefusesTheOwnHome(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	other := t.TempDir()

	for i := 0; i < 3; i++ {
		if err := addSessionRoot(other); err != nil {
			t.Fatal(err)
		}
	}
	// Adopting the home already scanned would double every session in the list.
	if err := addSessionRoot(home); err != nil {
		t.Fatal(err)
	}

	roots := loadSessionRoots()
	if len(roots) != 2 {
		t.Fatalf("roots = %+v, want own home + one adopted", roots)
	}
	if roots[1].Home != other || roots[1].Primary {
		t.Errorf("adopted root = %+v, want %q and NOT primary", roots[1], other)
	}
}

// A root that has gone away (unmounted disk, deleted account) is SKIPPED, not deleted: the user's
// decision to look there should survive the disk being unplugged.
func TestAVanishedRootIsSkippedNotForgotten(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	gone := filepath.Join(t.TempDir(), "unmounted")

	if err := addSessionRoot(gone); err != nil {
		t.Fatal(err)
	}
	if roots := loadSessionRoots(); len(roots) != 1 {
		t.Errorf("roots = %+v, want the vanished root skipped", roots)
	}
	// Still on disk, so it comes back when the path does.
	b, err := os.ReadFile(filepath.Join(daemonDir(), rootsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "unmounted") {
		t.Error("the vanished root was forgotten rather than skipped")
	}
}

// THE ONE THAT MAKES ADOPTED SESSIONS ACTUALLY WORK. Resuming under the ambient HOME means the
// engine resolves its store from the wrong home, finds nothing, and reports a session that plainly
// exists as missing.
func TestAnAdoptedRootResumesWithItsOwnHome(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	other := t.TempDir()

	env := resumeEnv(other)
	seen := 0
	for _, kv := range env {
		if len(kv) > 5 && kv[:5] == "HOME=" {
			seen++
			if kv != "HOME="+other {
				t.Errorf("HOME = %q, want %q", kv, "HOME="+other)
			}
		}
	}
	if seen != 1 {
		t.Errorf("found %d HOME entries, want exactly 1 — a duplicate makes the winner shell-dependent", seen)
	}
}

// The primary root must not carry an override at all: touching the environment for the common case
// risks changing behaviour for every session that works today.
func TestThePrimaryRootLeavesTheEnvironmentAlone(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)

	for _, root := range []string{"", home} {
		if got, want := len(resumeEnv(root)), len(os.Environ()); got != want {
			t.Errorf("resumeEnv(%q) changed the env (%d vs %d)", root, got, want)
		}
	}
}

// Detection must only ever offer a home whose store this process can actually READ. Readability is
// the privacy boundary — the OS has already decided, and partyline should not second-guess it in
// either direction.
func TestHasSessionStoreNeedsARealReadableStore(t *testing.T) {
	empty := t.TempDir()
	if hasSessionStore(empty) {
		t.Error("an empty directory was treated as holding sessions")
	}

	// A store directory that exists but is EMPTY is not evidence of sessions either — offering it
	// would send someone to a home with nothing in it.
	bare := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bare, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasSessionStore(bare) {
		t.Error("an empty .claude/projects was treated as holding sessions")
	}

	real := t.TempDir()
	dir := filepath.Join(real, ".claude", "projects", "encoded-cwd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abc.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasSessionStore(real) {
		t.Error("a home with a populated store was not detected")
	}
}

// THE LINK BETWEEN DETECTION AND RESUME, and it was untested until a mutation went unnoticed:
// deleting the tagging in collectSessions left every test green. Without the tag, an adopted
// session resumes under the ambient HOME, the engine looks in the wrong store, and a session that
// plainly exists is reported missing — the exact bug this whole change fixes, reintroduced.
func TestAdoptedSessionsAreTaggedWithTheirRoot(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	withHome(t, home)

	writeClaude := func(root, id, cwd string) {
		t.Helper()
		dir := filepath.Join(root, ".claude", "projects", "enc")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"user","sessionId":"` + id + `","cwd":"` + cwd + `","message":{"content":"hello"}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeClaude(home, "own-session", "/tmp/a")
	writeClaude(other, "adopted-session", "/tmp/b")
	if err := addSessionRoot(other); err != nil {
		t.Fatal(err)
	}

	byID := map[string]aiSession{}
	for _, s := range collectSessions() {
		byID[s.ID] = s
	}

	own, ok := byID["own-session"]
	if !ok {
		t.Fatal("the session in the process's own home vanished — the base case must never break")
	}
	if own.root != "" {
		t.Errorf("own session root = %q, want empty (it resumes with the ambient environment)", own.root)
	}

	ad, ok := byID["adopted-session"]
	if !ok {
		t.Fatal("the session under the adopted root was not found — the widened scan is not working")
	}
	if ad.root != other {
		t.Fatalf("adopted session root = %q, want %q — without this it resumes under the wrong HOME", ad.root, other)
	}
	// And end to end: the tag must actually produce the right resume environment.
	found := false
	for _, kv := range resumeEnv(ad.root) {
		if kv == "HOME="+other {
			found = true
		}
	}
	if !found {
		t.Error("the tagged root did not yield HOME pointing at it")
	}
}
