package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A stamp written now reads as live; the same stamp read after its window reads as dark. The light
// must go out on its own — nothing clears it, so an expiry that never fires would leave every session
// permanently lit and the marker would stop meaning anything.
func TestToolActivityGoesOutOnItsOwn(t *testing.T) {
	withHomeDir(t)
	t.Setenv("PARTYLINE_SESSION_KEY", "s-1")

	noteToolActivity("recall")
	tool, live := readToolActivity("s-1")
	if !live || tool != "recall" {
		t.Fatalf("a call just made should be live; got %q live=%v", tool, live)
	}

	// Age the stamp past its window rather than sleeping for it.
	old := time.Now().Add(-toolActivityFresh - time.Second)
	if err := os.Chtimes(toolActivityPath("s-1"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, live := readToolActivity("s-1"); live {
		t.Error("a stale stamp still reads as live — the marker would never go dark")
	}
}

// One session's calls must never light another's tab. The stamp is per-session because several
// cg-mcp servers write at once, one per live session.
func TestToolActivityIsPerSession(t *testing.T) {
	withHomeDir(t)
	t.Setenv("PARTYLINE_SESSION_KEY", "s-a")
	noteToolActivity("remember")

	if _, live := readToolActivity("s-b"); live {
		t.Error("a call in one session lit a different session's marker")
	}
	if _, live := readToolActivity("s-a"); !live {
		t.Error("the session that made the call is not lit")
	}
}

// No session key (a bare shell, or anything spawned outside the mux) must be a silent no-op, not a
// panic and not a write to a path derived from an empty string.
func TestToolActivityWithoutASessionKeyIsANoOp(t *testing.T) {
	withHomeDir(t)
	t.Setenv("PARTYLINE_SESSION_KEY", "")
	noteToolActivity("recall") // must not panic

	if _, live := readToolActivity(""); live {
		t.Error("an empty key resolved to a live stamp")
	}
	if entries, err := os.ReadDir(toolActivityDir()); err == nil && len(entries) > 0 {
		t.Errorf("a keyless call wrote %d file(s) into the activity dir", len(entries))
	}
}

// The key becomes a PATH, so a key containing a separator must not be able to write outside the
// activity directory.
func TestToolActivityRefusesAKeyThatCouldEscapeItsDirectory(t *testing.T) {
	withHomeDir(t)
	for _, key := range []string{"../escaped", "a/b", `a\b`} {
		if got := toolActivityPath(key); got != "" {
			t.Errorf("key %q resolved to %q; a separator must resolve to nothing", key, got)
		}
	}
}

// A relaunch under a recycled key must start dark rather than inherit its predecessor's light.
func TestForgettingASessionClearsItsLight(t *testing.T) {
	withHomeDir(t)
	t.Setenv("PARTYLINE_SESSION_KEY", "s-recycled")
	noteToolActivity("read_run")
	if _, live := readToolActivity("s-recycled"); !live {
		t.Fatal("precondition: the session should be lit")
	}

	forgetToolActivity("s-recycled")
	if _, live := readToolActivity("s-recycled"); live {
		t.Error("a forgotten session is still lit — a relaunch would inherit it")
	}
	if _, err := os.Stat(filepath.Join(toolActivityDir(), "s-recycled")); !os.IsNotExist(err) {
		t.Error("the stamp file survived forgetToolActivity")
	}
}

// EVERY partyline tool call must light the marker, which means the stamp has to be taken at the ONE
// place all calls pass through, before any per-tool branching. If it were moved into the individual
// handlers, the next tool added would silently not light.
func TestEveryToolCallIsStampedAtTheSingleDispatchPoint(t *testing.T) {
	src, err := os.ReadFile("cg_mcp.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	call := body[strings.Index(body, "func (s *cgServer) handleCall"):]
	stamp := strings.Index(call, "noteToolActivity(")
	if stamp < 0 {
		t.Fatal("handleCall never stamps activity — no partyline tool call would light its session")
	}
	// Before the first dispatch, or tools handled early (the org-scoped ones) would never light.
	if firstCase := strings.Index(call, "switch p.Name"); firstCase >= 0 && stamp > firstCase {
		t.Error("the stamp is taken after dispatch begins — tools handled early would not light their session")
	}
}
