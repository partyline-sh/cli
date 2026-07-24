package main

import "testing"

func TestWorkerTools(t *testing.T) {
	base := workerTools(false)
	for _, want := range []string{"Read", "Edit", "Write"} {
		if !hasStr(base, want) {
			t.Fatalf("default allowlist missing %s: %v", want, base)
		}
	}
	if hasStr(base, "Bash") {
		t.Fatal("Bash must be OFF by default (invariant 4)")
	}
	if !hasStr(workerTools(true), "Bash") {
		t.Fatal("--allow-bash must add Bash")
	}
}

func TestWorkerPrompt(t *testing.T) {
	p := workerPrompt("do a thing", "", true, false)
	// The non-negotiable safety instructions must be in the prompt.
	for _, want := range []string{"do a thing", "worktree", "human reviews and merges", "STOP", "remember"} {
		if !containsSub(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	if containsSub(workerPrompt("x", "", false, false), "remember") {
		t.Fatal("no-thread prompt should not mention the remember tool")
	}
	// Tool posture must be stated up front so the worker never discovers it by hitting denials.
	if !containsSub(workerPrompt("x", "", false, false), "do NOT have shell/Bash") {
		t.Fatal("no-bash prompt must tell the worker it has no shell access")
	}
	if !containsSub(workerPrompt("x", "", false, true), "VERIFY your change") {
		t.Fatal("bash-enabled prompt must tell the worker to verify by running tests")
	}
}

func TestWorkerPromptInjectsContext(t *testing.T) {
	ctx := "What this is about:\n• #12 [decision] use Postgres for the store  — user:me"
	p := workerPrompt("build the thing", ctx, true, false)
	// The injected context must appear, framed as background (not a task).
	for _, want := range []string{"Shared team context", "background you already know", "use Postgres for the store", "trust the code"} {
		if !containsSub(p, want) {
			t.Fatalf("context-injected prompt missing %q:\n%s", want, p)
		}
	}
	// The task still comes first, before the injected context.
	if idxOf(p, "build the thing") > idxOf(p, "Shared team context") {
		t.Fatal("the task should precede the injected context block")
	}
	// No context → no context header (clean prompt for a threadless run).
	if containsSub(workerPrompt("x", "", false, false), "Shared team context") {
		t.Fatal("empty context must not emit the context header")
	}
}

func idxOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func hasStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
