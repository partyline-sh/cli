package main

import (
	"os"
	"strings"
	"testing"
)

// The card's token section reads reviews from their files and the rest from usage.jsonl, and
// totals them by model and role.
func TestLoopUsageTable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "u1"
	if err := os.MkdirAll(loopDir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLoopJSON(id, "review-r1-1-claude_opus_medium.json", loopReview{Engine: "claude:opus:medium", Tokens: loopTokens{In: 47000, Out: 12000, Cached: 380000, CostUSD: 0.70}})
	writeLoopJSON(id, "review-b1-1-claude_opus_medium.json", loopReview{Engine: "claude:opus:medium", Tokens: loopTokens{In: 25000, Out: 8000, Cached: 330000, CostUSD: 0.42}})
	writeLoopJSON(id, "review-abc123.result.json", loopReview{Engine: "claude:opus:medium", Tokens: loopTokens{In: 1}}) // a contractor's copy: not counted twice
	recordLoopUsage(id, loopUsage{Role: "builder", Round: 1, Build: 1, Engine: "codex:gpt-6.1-sol:medium", Tokens: loopTokens{In: 30000, Out: 9000, Cached: 900000}})
	recordLoopUsage(id, loopUsage{Role: "builder", Round: 2, Build: 2, Engine: "codex:gpt-6.1-sol:medium", Tokens: loopTokens{In: 10000, Out: 1000, Cached: 100000}})
	recordLoopUsage(id, loopUsage{Role: "arbiter", Engine: "claude:opus:medium"}) // no tokens reported: not recorded
	got := usageTable(loopUsages(id))
	for _, want := range []string{
		"claude:opus:medium reviewer · 1 run · 59.0k fresh, 380.0k cached · $0.70",
		"claude:opus:medium brief reviewer · 1 run · 33.0k fresh, 330.0k cached · $0.42",
		"codex:gpt-6.1-sol:medium builder · 2 runs · 50.0k fresh, 1.0M cached\n",
		"total · 142.0k fresh, 1.7M cached · $1.12",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "arbiter") {
		t.Errorf("an arbiter run with no tokens is listed:\n%s", got)
	}
}

func TestUnwrapShell(t *testing.T) {
	for in, want := range map[string]string{
		`/bin/zsh -lc 'cat docs/plans/mobile-pwa.md'`: "cat docs/plans/mobile-pwa.md",
		`/bin/zsh -lc "go test ./... 2>&1 | tail`:     "go test ./... 2>&1 | tail", // clipped by the live view
		"Bash go vet ./...":                           "Bash go vet ./...",
	} {
		if got := unwrapShell(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
