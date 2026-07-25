package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

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

func TestSelectContextBlocks(t *testing.T) {
	blocks := []api.ContextBlock{
		{ID: 1, Kind: "overview", Body: "the project overview", Status: "open"},
		{ID: 2, Kind: "decision", Body: "relay uses Noise", Entities: []string{"dir:internal/relay"}, Status: "open"},
		{ID: 3, Kind: "constraint", Body: "billing is single-workspace", Entities: []string{"concept:billing"}, Status: "open"},
		{ID: 4, Kind: "contract", Body: "superseded fact", Entities: []string{"dir:internal/relay"}, Status: "superseded"},
		{ID: 5, Kind: "decision", Body: "newest unrelated", Entities: []string{"pkg:dnd-kit"}, Status: "open"},
	}
	// A task about the relay dir → block #2 (entity dir:internal/relay) should rank ahead of
	// unrelated facts; the overview is always kept; the superseded block is dropped.
	got := selectContextBlocks(blocks, "fix a bug in internal/relay handshake", 1<<20)
	ids := make([]int64, len(got))
	for i, b := range got {
		ids[i] = b.ID
	}
	// overview (#1) first, then matched #2, then the unmatched by recency (#5 before #3 — higher id).
	want := []int64{1, 2, 5, 3}
	if len(ids) != len(want) {
		t.Fatalf("want %v, got %v", want, ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ordering wrong: want %v, got %v", want, ids)
		}
	}
	for _, b := range got {
		if b.Status == "superseded" {
			t.Fatal("superseded block must be dropped")
		}
	}
}

func TestSelectContextBlocksBudget(t *testing.T) {
	// Tiny budget: the overview is admitted (first block always), then ranked fills until full.
	blocks := []api.ContextBlock{
		{ID: 1, Kind: "overview", Body: strings.Repeat("x", 200), Status: "open"},
		{ID: 2, Kind: "decision", Body: strings.Repeat("y", 200), Entities: []string{"concept:auth"}, Status: "open"},
		{ID: 3, Kind: "decision", Body: strings.Repeat("z", 200), Entities: []string{"concept:auth"}, Status: "open"},
	}
	got := selectContextBlocks(blocks, "work on auth", 300)
	if len(got) == 0 || got[0].ID != 1 {
		t.Fatalf("overview must always be first-admitted; got %v", got)
	}
	if len(got) == 3 {
		t.Fatal("budget of 300 should not fit all three ~250-byte blocks")
	}
}

func TestBlockRelevanceEngineAgnostic(t *testing.T) {
	// Pure string scoring — no model, no engine. A typed anchor named in the task scores; an
	// unrelated one doesn't. (This is what keeps selection identical across claude/codex/gemini.)
	b := api.ContextBlock{Entities: []string{"file:web/src/lib/api/runs.ts"}}
	if blockRelevance(b, "edit web/src/lib/api/runs.ts to add a field") == 0 {
		t.Fatal("a task naming the anchored file should score > 0")
	}
	if blockRelevance(api.ContextBlock{Entities: []string{"concept:telemetry"}}, "fix the login button") != 0 {
		t.Fatal("an unrelated anchor should score 0")
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
