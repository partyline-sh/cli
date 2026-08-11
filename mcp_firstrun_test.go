package main

import (
	"errors"
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

// The nag. Reported from a screenshot of `ptln update` asking about Antigravity — an engine whose
// connect could NEVER succeed, so it was never recorded, so it was asked about on every single
// invocation. Two bugs met: a permanently-failing connect, and a state file that only remembered
// successes.
func TestAFailedConnectIsStillAnAnswer(t *testing.T) {
	// The RULE, not a fixture: a connect that errored must still produce an answer. Recording only
	// successes is what asked about Antigravity on every single invocation, because its connect
	// could never succeed and so was never recorded.
	if got := outcomeFor(errors.New("agy plugin import: exit status 1")); got == "" {
		t.Fatal("a failed connect recorded nothing — it will be asked about forever")
	}
	if outcomeFor(nil) != "connected" {
		t.Fatal("a successful connect must record as connected")
	}

	// …and whatever it records must take the engine out of the pending set.
	withTempHome(t)
	st := loadMCPOffer()
	st.Answered["antigravity"] = outcomeFor(errors.New("boom"))
	saveMCPOffer(st)
	for _, e := range pendingEngines(loadMCPOffer()) {
		if e.key == "antigravity" {
			t.Fatal("a failed connect came back as pending — this is the every-invocation nag")
		}
	}
}

func TestAlreadyRegisteredCountsAsSuccess(t *testing.T) {
	// Every engine CLI exits non-zero when the server is already in its config, which is the state
	// we wanted. Antigravity's chain is `claude mcp add` THEN `agy plugin import`, so once claude
	// was wired the add "failed", the import never ran, and antigravity could never succeed again.
	for _, out := range []string{
		"MCP server partyline-context-threads already exists in user config",
		"Server already registered",
		"already configured",
		"MCP SERVER ALREADY EXISTS",
	} {
		if !alreadyRegistered(out) {
			t.Errorf("should read as success: %q", out)
		}
	}
}

func TestARealFailureIsNotSwallowed(t *testing.T) {
	// The narrowness matters as much as the match: swallowing a genuine error would report a wiring
	// that never happened, and the tools would simply be absent with no explanation.
	for _, out := range []string{
		"", "command not found", "permission denied",
		"failed to write config", "unexpected error: EACCES",
	} {
		if alreadyRegistered(out) {
			t.Errorf("must NOT read as success: %q", out)
		}
	}
}

func TestThePromptOnlyFiresAtTheFrontDoor(t *testing.T) {
	// The screenshot was `ptln update` — someone came to do one specific thing and got an unrelated
	// setup question. Even a once-only prompt feels like nagging when it interrupts something else.
	realArgs := os.Args
	t.Cleanup(func() { os.Args = realArgs })

	for _, argv := range [][]string{
		{"ptln"}, {"ptln", "login"}, {"ptln", "llms"}, {"ptln", "--resume"},
	} {
		os.Args = argv
		if !frontDoorInvocation() {
			t.Errorf("%v is a front door and should be allowed to ask", argv)
		}
	}
	for _, argv := range [][]string{
		{"ptln", "update"}, {"ptln", "crank"}, {"ptln", "wt"}, {"ptln", "version"},
		{"ptln", "daemon"}, {"ptln", "--help"}, {"ptln", "chat"},
	} {
		os.Args = argv
		if frontDoorInvocation() {
			t.Errorf("%v must NOT interrupt with a setup question", argv)
		}
	}
}
