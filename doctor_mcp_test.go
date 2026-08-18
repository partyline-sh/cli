package main

import "testing"

// The wiring check exists because configuration can LOOK right while being broken (#558): the
// v0.37.x incident registered a /tmp binary into three engine configs, and nothing could say so.
// These tests pin the parsing of each engine's real config shape and the verdict rule.

func TestMCPCommandFromClaudeJSON(t *testing.T) {
	registered := []byte(`{"mcpServers":{"partyline-context-threads":{"command":"/opt/homebrew/bin/ptln","args":["cg-mcp"]},"other":{"command":"/x"}}}`)
	if got := mcpCommandFromClaudeJSON(registered); got != "/opt/homebrew/bin/ptln" {
		t.Errorf("registered: got %q", got)
	}
	if got := mcpCommandFromClaudeJSON([]byte(`{"mcpServers":{"other":{"command":"/x"}}}`)); got != "" {
		t.Errorf("unregistered: got %q, want empty", got)
	}
	if got := mcpCommandFromClaudeJSON([]byte(`not json`)); got != "" {
		t.Errorf("garbage: got %q, want empty", got)
	}
	if got := mcpCommandFromClaudeJSON(nil); got != "" {
		t.Errorf("missing file: got %q, want empty", got)
	}
}

func TestMCPCommandFromCodexTOML(t *testing.T) {
	registered := []byte("[mcp_servers.other]\ncommand = \"/y\"\n\n[mcp_servers.partyline-context-threads]\ncommand = \"/opt/homebrew/bin/ptln\"\nargs = [\"cg-mcp\"]\n")
	if got := mcpCommandFromCodexTOML(registered); got != "/opt/homebrew/bin/ptln" {
		t.Errorf("registered: got %q", got)
	}
	// The anchor must not leak across section boundaries: our block absent, another server's
	// command present — must NOT match.
	other := []byte("[mcp_servers.other]\ncommand = \"/y\"\n")
	if got := mcpCommandFromCodexTOML(other); got != "" {
		t.Errorf("other-only: got %q, want empty", got)
	}
	if got := mcpCommandFromCodexTOML(nil); got != "" {
		t.Errorf("missing: got %q, want empty", got)
	}
}

func TestMCPWiringVerdict(t *testing.T) {
	if st, _ := mcpWiringVerdict("", false); st != ckFail {
		t.Error("unwired must FAIL")
	}
	// The v0.37.x ghost: registered path, file gone. Must FAIL, not pass on "it's configured".
	if st, d := mcpWiringVerdict("/tmp/gone/ptln", false); st != ckFail || d == "" {
		t.Error("wired-to-a-ghost must FAIL with the path named")
	}
	if st, _ := mcpWiringVerdict("/private/tmp/ptln-dev", true); st != ckWarn {
		t.Error("an ephemeral-but-present path must WARN")
	}
	if st, _ := mcpWiringVerdict("/opt/homebrew/bin/ptln", true); st != ckPass {
		t.Error("a real registered path must PASS")
	}
}
