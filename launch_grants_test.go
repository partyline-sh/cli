package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// Grants are the seam where control-plane DATA meets local capability — the tests pin the
// security posture: strict re-validation, local-catalog-only resolution, fail-closed skips.
func TestResolveLaunchGrants(t *testing.T) {
	cat := mcpCatalog{
		"linear": {Command: "npx", Args: []string{"-y", "linear-mcp"}},
		"docs":   {URL: "https://mcp.example.com", Headers: map[string]string{"x": "y"}},
	}

	t.Run("nil grants → nothing", func(t *testing.T) {
		allow, cfg, notes := resolveLaunchGrants(nil, cat)
		if allow != nil || cfg != "" || notes != nil {
			t.Fatalf("nil grants must be inert: %v %q %v", allow, cfg, notes)
		}
	})

	t.Run("shell prefixes → Bash rules", func(t *testing.T) {
		allow, _, notes := resolveLaunchGrants(&api.ToolGrants{Shell: []string{"gh *", "git log *", "gh api user"}}, cat)
		want := []string{"Bash(gh:*)", "Bash(git log:*)", "Bash(gh api user)"}
		if strings.Join(allow, ",") != strings.Join(want, ",") {
			t.Fatalf("allow = %v, want %v", allow, want)
		}
		if len(notes) != 0 {
			t.Fatalf("unexpected notes: %v", notes)
		}
	})

	t.Run("metacharacters and junk are skipped, never widened", func(t *testing.T) {
		bad := []string{"gh; rm -rf /", "gh && curl x", "gh | sh", "$(gh)", "gh > /etc/passwd", "../bin/sh *", ""}
		allow, _, notes := resolveLaunchGrants(&api.ToolGrants{Shell: bad}, cat)
		if len(allow) != 0 {
			t.Fatalf("injection shapes must all be skipped: %v", allow)
		}
		if len(notes) != len(bad) {
			t.Fatalf("every skip must be audited: %d notes for %d bad entries", len(notes), len(bad))
		}
	})

	t.Run("mcp names resolve against the LOCAL catalog only", func(t *testing.T) {
		allow, cfg, notes := resolveLaunchGrants(&api.ToolGrants{MCP: []string{"linear", "ghost-server", "not a name!"}}, cat)
		if strings.Join(allow, ",") != "mcp__linear" {
			t.Fatalf("only locally-known servers may be allowed: %v", allow)
		}
		if !strings.Contains(cfg, `"linear"`) || strings.Contains(cfg, "ghost") {
			t.Fatalf("config must hold exactly the resolved server: %s", cfg)
		}
		if len(notes) != 2 {
			t.Fatalf("unknown + invalid names must both be audited: %v", notes)
		}
	})

	t.Run("caps mirror the web (truncate, note)", func(t *testing.T) {
		many := make([]string, maxGrantEntries+5)
		for i := range many {
			many[i] = "gh *"
		}
		allow, _, notes := resolveLaunchGrants(&api.ToolGrants{Shell: many}, cat)
		if len(allow) != maxGrantEntries {
			t.Fatalf("must truncate to %d, got %d", maxGrantEntries, len(allow))
		}
		if len(notes) == 0 {
			t.Fatal("truncation must be audited")
		}
	})
}
