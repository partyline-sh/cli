package main

import (
	"context"
	"slices"
	"testing"
)

// Every headless claude run is restricted, has no MCP servers and states its tools. Without these a
// run executes the repository's hooks, reads outside its directory, and can call the user's MCP
// servers.
func TestHeadlessClaudeIgnoresRepositorySettings(t *testing.T) {
	cmd := headlessClaude(context.Background(), t.TempDir(), "hi", "", "--model", "haiku")
	args := cmd.Args[1:]
	for _, flag := range []string{"--restricted", "--strict-mcp-config"} {
		if !slices.Contains(args, flag) {
			t.Errorf("no %s: %q", flag, args)
		}
	}
	j := slices.Index(args, "--tools")
	if j < 0 || j+1 >= len(args) || args[j+1] != "" {
		t.Errorf("tools not stated: %q", args)
	}
	if cmd.Stdin != nil {
		t.Error("stdin should be /dev/null")
	}
}
