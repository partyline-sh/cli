package main

import (
	"encoding/json"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// getPrompt drives prompts/get through the real JSON-RPC entry point and returns the single message.
func getPrompt(t *testing.T, s *cgServer, name string) string {
	t.Helper()
	var got promptResult
	if err := json.Unmarshal([]byte(rpc(t, s, "prompts/get", map[string]any{"name": name})), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != nil {
		t.Fatalf("prompts/get %s errored: %+v", name, got.Error)
	}
	if len(got.Result.Messages) != 1 {
		t.Fatalf("%s: want exactly one message, got %d", name, len(got.Result.Messages))
	}
	return got.Result.Messages[0].Content.Text
}

// THE PROPERTY THAT MATTERS MOST. Setup is what you run when NOTHING is configured — no token, no
// bound thread, possibly not even signed in. Every other prompt here answers `cgNoThread` in that
// state, which is correct for them and would be useless here: a setup prompt that refuses until
// setup is done helps nobody. HOME is a temp dir so there is no token and no bind to fall back on.
func TestSetupPromptsAnswerWithNoThreadAndNoToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &cgServer{c: api.New()}

	for _, name := range []string{setupPromptName, setupSelfHostPromptName} {
		text := getPrompt(t, s, name)
		if strings.Contains(text, cgNoThread) {
			t.Fatalf("%s returned the no-thread refusal — setup must work before a thread exists:\n%s", name, text)
		}
		if len(text) < 400 {
			t.Fatalf("%s returned %d chars — that is not a walkthrough", name, len(text))
		}
	}
}

// A prompt nobody is offered is a prompt nobody runs.
func TestSetupPromptsAreListed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &cgServer{c: api.New()}
	listed := rpc(t, s, "prompts/list", nil)
	for _, name := range []string{setupPromptName, setupSelfHostPromptName} {
		if !strings.Contains(listed, name) {
			t.Fatalf("%s is not listed:\n%s", name, listed)
		}
	}
}

// THE SAFETY RULES ARE THE FEATURE. This prompt points an agent at a stranger's machine, so the
// instructions that keep it from writing files unattended or spilling a secret into a transcript
// are load-bearing text, not tone. Assert they survive an edit.
func TestSetupPromptsCarryTheSafetyRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &cgServer{c: api.New()}

	for _, name := range []string{setupPromptName, setupSelfHostPromptName} {
		text := getPrompt(t, s, name)
		for _, want := range []string{
			"NEVER generate",          // no secret values, ever
			"openssl rand -base64 32", // the human generates them, not the agent
			"ONE STEP AT A TIME",
			"STOP at the first failing check",
			"approved that specific step",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s is missing the safety rule %q — an agent following it could write to a box unattended", name, want)
			}
		}
	}
}

// Each path must point at the command that gives it real state, rather than reciting a plan from
// memory. The client path's floor is `ptln doctor`; the server path's is `bootstrap --json`.
func TestSetupPromptsUseTheRealChecks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &cgServer{c: api.New()}

	client := getPrompt(t, s, setupPromptName)
	if !strings.Contains(client, "ptln doctor") {
		t.Error("the client setup prompt never runs `ptln doctor` — it would be guessing at what is already configured")
	}

	server := getPrompt(t, s, setupSelfHostPromptName)
	if !strings.Contains(server, "ptln server bootstrap --json") {
		t.Error("the self-host prompt never runs `ptln server bootstrap --json` — the plan is not its to invent")
	}
	if !strings.Contains(server, "ptln server doctor") {
		t.Error("the self-host prompt never verifies with `ptln server doctor` — it would report the plan's intent as the outcome")
	}
}

// The rejected alternative. `bootstrap --apply` was turned down because it puts secret generation
// and file writes in a tool's hands on a machine we do not own; if a future edit teaches the prompt
// to ask for it, this fails and the decision gets re-made deliberately rather than by accident.
func TestSetupPromptsNeverReachForAnApplyFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &cgServer{c: api.New()}
	for _, name := range []string{setupPromptName, setupSelfHostPromptName} {
		if text := getPrompt(t, s, name); strings.Contains(text, "--apply") {
			t.Errorf("%s tells the agent to use --apply, which was rejected by design", name)
		}
	}
}
