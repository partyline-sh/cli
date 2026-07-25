package main

import (
	"bytes"
	"encoding/json"
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
