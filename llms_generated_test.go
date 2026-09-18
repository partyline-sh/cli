package main

import "testing"

// The presentation rule from Matt's first-install screen: an agent framework's ULID workspace
// dirs must not each render as a top-level "project" named by their ID.
func TestIDShapedName(t *testing.T) {
	yes := []string{
		"01KZE9HWBC3PY3K1HP2DQDBBR6", // ULID (the exact shape on screen)
		"01kzc6tjfen5nxvatp33ntnrrk",
		"8101a1d9-7a07-47a9-8ba8-229bea756106", // UUID
		"deadbeefdeadbeef",                     // long hex
		"ses_01KZE9HWBC3PY3K1HP2DQDBBR6",
	}
	no := []string{
		"partyline", "acr-pos-3.2.1", "trade-journal", "TJ", "matthew",
		"hoops-dashboard", "deadbeef", // short hex stays a name
		"0123456789", ".openclaw",
	}
	for _, n := range yes {
		if !idShapedName(n) {
			t.Errorf("idShapedName(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if idShapedName(n) {
			t.Errorf("idShapedName(%q) = true, want false", n)
		}
	}
}

func TestCollapseGeneratedKey(t *testing.T) {
	cases := map[string]string{
		// The OpenClaw shape: per-session ULID dirs under a workspace container.
		"/home/matthew/.openclaw/workspace/01KZE9HWBC3PY3K1HP2DQDBBR6": "/home/matthew/.openclaw",
		// A bare ID dir directly under a project keeps the project.
		"/home/matthew/dev/tool/01KZE9HWBC3PY3K1HP2DQDBBR6": "/home/matthew/dev/tool",
		// Human-named dirs are untouched.
		"/Users/darcy/dev/partyline": "/Users/darcy/dev/partyline",
		"/home/matthew":              "/home/matthew",
		"":                           "",
	}
	for in, want := range cases {
		if got := collapseGeneratedKey(in); got != want {
			t.Errorf("collapseGeneratedKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBoilerplateTitleRejectsFrameworkPreambles(t *testing.T) {
	for _, s := range []string{
		"Workspace boundary (important): - Confine source, project, user-data, and system file changes",
		"[structured-output-enforce] You MUST call the StructuredOutput tool to complete this request.",
	} {
		if cleanTitle(s) != "" {
			t.Errorf("cleanTitle(%q) = %q, want empty (boilerplate)", s, cleanTitle(s))
		}
	}
	if cleanTitle("fix the workspace boundary docs") == "" {
		t.Error("a real request mentioning the words mid-sentence must survive")
	}
}
