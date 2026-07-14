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
	p := workerPrompt("do a thing", true)
	// The non-negotiable safety instructions must be in the prompt.
	for _, want := range []string{"do a thing", "worktree", "human reviews and merges", "STOP", "remember"} {
		if !containsSub(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
	if containsSub(workerPrompt("x", false), "remember") {
		t.Fatal("no-thread prompt should not mention the remember tool")
	}
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
