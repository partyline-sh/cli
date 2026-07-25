package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// The Planning agent must be discoverable under its real name (with describe kept as the legacy
// alias), and the bulk plan_file_tree tool must be registered — the CLI half of "planning agent
// everywhere" (#573).
func TestPlanningAgentSurfaceRegistered(t *testing.T) {
	names := make([]string, 0, len(cgPromptDefs))
	for _, p := range cgPromptDefs {
		names = append(names, p["name"].(string))
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"planning_agent", "describe"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("prompt %q not registered (have: %s)", want, joined)
		}
	}
	toolNames := make([]string, 0, len(cgToolDefs))
	for _, d := range cgToolDefs {
		toolNames = append(toolNames, d["name"].(string))
	}
	if !strings.Contains(strings.Join(toolNames, ","), "plan_file_tree") {
		t.Fatalf("plan_file_tree tool not registered (have: %v)", toolNames)
	}
}

// plan_file_tree must fail fast, with an actionable message, on a rootless call — BEFORE any
// network I/O (the server-side validation handles the rest and its errors pass through verbatim).
func TestPlanFileTreeValidatesRoot(t *testing.T) {
	s := &cgServer{c: api.New(), thread: "t-1"}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	params, _ := json.Marshal(map[string]any{
		"name":      "plan_file_tree",
		"arguments": map[string]any{"root": map[string]any{"title": "", "kind": ""}},
	})
	s.handleCall(enc, rpcReq{ID: json.RawMessage(`1`), Params: params})
	if !strings.Contains(out.String(), "needs a `root`") {
		t.Fatalf("expected the actionable validation error, got: %s", out.String())
	}
}

// Zero-config MCP: the server boots in every session, so the thread must resolve LAZILY — a repo
// bound AFTER the session opened (the exact race the founder hit live: restart at 20:57, bind at
// 21:00) is picked up at the next call, with no restart. Once resolved it stays resolved.
func TestResolveThreadLazily(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no login token → markConnected stays a no-op (no network)
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	t.Chdir(repo)

	s := &cgServer{c: api.New()}
	if s.resolveThread(); s.thread != "" {
		t.Fatalf("no bind yet — must stay threadless, got %q", s.thread)
	}
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"thread":"t-lazy-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.resolveThread(); s.thread != "t-lazy-1" {
		t.Fatalf("bind written after boot must be picked up, got %q", s.thread)
	}
	// Cached: a later bind change never re-points a live session mid-conversation.
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"thread":"t-other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.resolveThread(); s.thread != "t-lazy-1" {
		t.Fatalf("resolved thread must stay stable for the session, got %q", s.thread)
	}
}
