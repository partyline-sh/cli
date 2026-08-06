package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The state file is what stops a helpful prompt becoming an every-launch nag — the failure mode
// that makes people reach for a mute rather than an answer.

func withTempHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestAnAnsweredEngineIsNeverAskedAgain(t *testing.T) {
	withTempHome(t)
	st := loadMCPOffer()
	st.Answered["claude"] = "connected"
	saveMCPOffer(st)

	got := loadMCPOffer()
	if got.Answered["claude"] != "connected" {
		t.Fatalf("answer did not survive a round trip: %+v", got.Answered)
	}
	for _, e := range pendingEngines(got) {
		if e.key == "claude" {
			t.Fatal("claude is still pending after being answered — this is the every-launch nag")
		}
	}
}

func TestDecliningIsRememberedToo(t *testing.T) {
	// A "no" that is not recorded is a "no" you get asked again tomorrow, which teaches people to
	// stop reading the prompt.
	withTempHome(t)
	st := loadMCPOffer()
	st.Answered["gemini"] = "declined"
	saveMCPOffer(st)

	for _, e := range pendingEngines(loadMCPOffer()) {
		if e.key == "gemini" {
			t.Fatal("a declined engine came back as pending")
		}
	}
}

func TestAnEngineInstalledLaterStillGetsOffered(t *testing.T) {
	// Answering about the engines you had must not permanently silence the question for an engine
	// you install next month — the state is per-engine for exactly this.
	withTempHome(t)
	st := loadMCPOffer()
	for _, e := range connectableEngines {
		st.Answered[e.key] = "connected"
	}
	delete(st.Answered, "codex")
	saveMCPOffer(st)

	loaded := loadMCPOffer()
	if loaded.Answered["codex"] != "" {
		t.Fatal("fixture is wrong — codex should be unanswered")
	}
	// pendingEngines intersects with what's installed, so assert the RULE rather than the machine:
	// codex is unanswered, so if it is installed it must be pending.
	for _, e := range installedEngines() {
		if e.key != "codex" {
			continue
		}
		found := false
		for _, p := range pendingEngines(loaded) {
			if p.key == "codex" {
				found = true
			}
		}
		if !found {
			t.Fatal("codex is installed and unanswered but was not offered")
		}
	}
}

func TestMissingStateFileMeansEverythingIsUnanswered(t *testing.T) {
	// First run on a fresh machine: no file, and every installed engine should be offered rather
	// than silently treated as already-handled.
	withTempHome(t)
	if _, err := os.Stat(filepath.Join(stateDir(), "mcp-offer.json")); !os.IsNotExist(err) {
		t.Skip("temp home already has state")
	}
	st := loadMCPOffer()
	if len(st.Answered) != 0 {
		t.Fatalf("fresh install should have no answers, got %+v", st.Answered)
	}
	if len(pendingEngines(st)) != len(installedEngines()) {
		t.Fatal("a fresh install should offer every installed engine")
	}
}

// Found by dogfooding this very PR: a `go build -o /tmp/…` smoke test wrote /tmp paths into three
// real engine configs. Engine configs store an ABSOLUTE path, so that registration dies silently
// the moment the file is cleaned up — and the symptom (the tools just stopped existing) points
// nowhere near the cause.
func TestRefusesToRegisterAThrowawayBinary(t *testing.T) {
	for _, p := range []string{"/tmp/ptln-build", "/private/tmp/x/ptln", "/var/folders/ab/T/go-build123/b001/exe/ptln"} {
		if !isEphemeralPath(p) {
			t.Errorf("%s should be refused — it will not survive", p)
		}
	}
}

func TestDoesNotRefuseARealInstall(t *testing.T) {
	// Conservative on purpose: refusing an unusual-but-real install location would block the whole
	// feature for someone whose setup we simply did not anticipate.
	for _, p := range []string{"/opt/homebrew/bin/ptln", "/usr/local/bin/ptln", "/home/me/.local/bin/ptln"} {
		if isEphemeralPath(p) {
			t.Errorf("%s is a real install path and must not be refused", p)
		}
	}
}

func TestTheEscapeHatchWorks(t *testing.T) {
	// Someone testing a dev build on purpose should be able to say so rather than be stuck.
	t.Setenv("PARTYLINE_ALLOW_TEMP_CONNECT", "1")
	if isEphemeralPath("/tmp/ptln-build") {
		t.Error("PARTYLINE_ALLOW_TEMP_CONNECT should override the refusal")
	}
}
