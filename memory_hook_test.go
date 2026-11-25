package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Sessions do not end — this machine has had agent sessions alive for a fortnight — so the
// refresh rides every session, and it must not disarm keep-going when both are configured.
// An earlier shape handed Claude a fresh settings object per feature, which would have silently
// dropped whichever was applied second.
func TestSessionSettingsMergesHooks(t *testing.T) {
	var plain map[string]any
	if err := json.Unmarshal([]byte(sessionSettings(nil)), &plain); err != nil {
		t.Fatal(err)
	}
	hooks, _ := plain["hooks"].(map[string]any)
	if _, ok := hooks["UserPromptSubmit"]; !ok {
		t.Fatal("every session must carry the memory refresh hook")
	}

	var both map[string]any
	if err := json.Unmarshal([]byte(keepGoingSettings("kg-1")), &both); err != nil {
		t.Fatal(err)
	}
	h2, _ := both["hooks"].(map[string]any)
	if _, ok := h2["Stop"]; !ok {
		t.Error("keep-going lost its Stop hook")
	}
	if _, ok := h2["UserPromptSubmit"]; !ok {
		t.Error("arming keep-going disarmed the memory refresh")
	}
	if !strings.Contains(keepGoingSettings("kg-1"), "keepgoing-hook --key kg-1") {
		t.Error("the keep-going key did not survive the merge")
	}
}

// Only what the session has not been told. Every turn asks; almost every turn has nothing to say.
func TestNewerThan(t *testing.T) {
	now := time.Now()
	facts := []fact{
		{ID: "c", At: now},
		{ID: "b", At: now.Add(-time.Hour)},
		{ID: "a", At: now.Add(-2 * time.Hour)},
	}
	got := newerThan(facts, now.Add(-90*time.Minute))
	if len(got) != 2 || got[0].ID != "c" || got[1].ID != "b" {
		t.Fatalf("newerThan returned %v", ids(got))
	}
	if n := len(newerThan(facts, now)); n != 0 {
		t.Errorf("nothing is newer than now, got %d", n)
	}
}

// A teammate who recorded twenty things overnight must not dump twenty into the middle of
// somebody's work.
func TestRefreshIsCapped(t *testing.T) {
	var many []fact
	for i := 0; i < memoryRefreshCap+6; i++ {
		many = append(many, fact{ID: string(rune('a' + i)), Kind: "decision", By: "Matt",
			At: time.Now(), Body: "thing " + string(rune('a'+i))})
	}
	out := renderRefresh("acr", "fleet-manager", many)
	if n := strings.Count(out, "- ["); n != memoryRefreshCap {
		t.Errorf("refresh carried %d facts, cap is %d", n, memoryRefreshCap)
	}
	if !strings.Contains(out, "and 6 more") {
		t.Errorf("a capped refresh must say what it held back:\n%s", out)
	}
	if !strings.Contains(out, "newer fact wins") {
		t.Error("a mid-session refresh must tell the agent it supersedes the earlier brief")
	}
}

func ids(fs []fact) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}
