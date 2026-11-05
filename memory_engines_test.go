package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// These settings files hold the user's MCP servers and their auth selection. Writing one from
// scratch to add a hook would silently sign them out of the engine — a far worse outcome than the
// missing hook it was fixing.
func TestInstallingAHookPreservesTheRestOfTheConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	orig := `{"mcpServers":{"partyline":{"command":"ptln"}},"security":{"auth":{"selectedType":"oauth-personal"}}}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installJSONHook(path, "BeforeAgent", "ptln memory-hook"); err != nil {
		t.Fatal(err)
	}
	m := mustJSON(t, path)
	if _, ok := m["mcpServers"].(map[string]any)["partyline"]; !ok {
		t.Error("the user's MCP server registration was dropped")
	}
	sec, _ := m["security"].(map[string]any)
	auth, _ := sec["auth"].(map[string]any)
	if auth["selectedType"] != "oauth-personal" {
		t.Error("the user's auth selection was dropped — this signs them out")
	}
	if !jsonHookInstalled(path, "BeforeAgent") {
		t.Error("the hook was not actually registered")
	}
}

// Installers get re-run. Stacking a duplicate hook means the brief is injected twice per turn.
func TestInstallingTwiceDoesNotStackHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for i := 0; i < 4; i++ {
		if err := installJSONHook(path, "BeforeAgent", "ptln memory-hook"); err != nil {
			t.Fatal(err)
		}
	}
	m := mustJSON(t, path)
	hooks, _ := m["hooks"].(map[string]any)
	entries, _ := hooks["BeforeAgent"].([]any)
	if len(entries) != 1 {
		t.Fatalf("expected 1 hook entry after 4 installs, got %d", len(entries))
	}
}

// A config we cannot parse is a config we must not overwrite.
func TestUnparseableConfigIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	junk := "{ this is not json"
	if err := os.WriteFile(path, []byte(junk), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installJSONHook(path, "BeforeAgent", "ptln memory-hook"); err == nil {
		t.Error("expected an error rather than a silent overwrite")
	}
	b, _ := os.ReadFile(path)
	if string(b) != junk {
		t.Errorf("the unreadable config was overwritten:\n%s", b)
	}
}

// Trust is inherited by subdirectories, so a trusted home covers every repo under it. Getting this
// wrong in the strict direction would nag about trust that is already granted.
func TestTrustIsInheritedBySubdirectories(t *testing.T) {
	roots := []any{"/Users/someone"}
	if got := dirCoveredBy(roots, "/Users/someone/dev/myrepo"); got != triYes {
		t.Errorf("a repo under a trusted root read as %s", got)
	}
	if got := dirCoveredBy(roots, "/Users/someone-else/dev"); got != triNo {
		t.Errorf("a sibling path with a shared prefix read as %s — prefix matching is not path-aware", got)
	}
	if got := dirCoveredBy("not a list", "/x"); got != triUnknown {
		t.Errorf("a malformed trust list read as %s rather than unknown", got)
	}
}

// Codex trust must never report "yes" on a guess. It stores trust as a hash we cannot recompute,
// and claiming a hook will run when it silently will not is the exact failure this guards.
func TestCodexTrustIsReportedAsUnknownNotAssumed(t *testing.T) {
	for _, s := range memoryHookStatus(t.TempDir()) {
		if s.engine != "codex" {
			continue
		}
		if s.trusted == triYes {
			t.Error("codex trust was asserted without being verifiable")
		}
		if s.detail == "" {
			t.Error("codex status does not warn that it degrades silently")
		}
	}
}

func mustJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
