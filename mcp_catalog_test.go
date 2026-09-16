package main

import (
	"reflect"
	"strings"
	"testing"
)

// The pure wiring helpers: build claude/codex flags from a catalog, and recognize what we
// manage (so strip-and-rebuild never eats a user's own flags).

func TestMCPServersJSON(t *testing.T) {
	cat := mcpCatalog{
		"context7": {Command: "npx", Args: []string{"-y", "@upstash/context7-mcp"}},
		"gh":       {URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}},
	}
	// thread + one stdio + one http server, all in ONE merged config
	got := mcpServersJSON(true, []string{"context7", "gh"}, cat)
	for _, want := range []string{`"partyline-context-threads"`, `"context7"`, `"npx"`, `"gh"`, `"type":"http"`, `"https://example.com/mcp"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged config missing %s:\n%s", want, got)
		}
	}
	// a name deleted from the catalog since it was toggled → dropped silently
	if got := mcpServersJSON(false, []string{"gone"}, cat); got != "" {
		t.Fatalf("dangling name should produce no config, got %s", got)
	}
	if got := mcpServersJSON(false, nil, cat); got != "" {
		t.Fatalf("no thread + no mcps should produce no config, got %s", got)
	}
}

func TestMCPCodexFlags(t *testing.T) {
	cat := mcpCatalog{
		"context7": {Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"K": "v"}},
		"httponly": {URL: "https://example.com"},
	}
	got := mcpCodexFlags([]string{"context7", "httponly"}, cat)
	joined := strings.Join(got, " ")
	for _, want := range []string{`mcp_servers.context7.command="npx"`, `mcp_servers.context7.args=["-y","pkg"]`, `mcp_servers.context7.env.K="v"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("codex flags missing %s:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "httponly") {
		t.Fatalf("http server must be skipped for codex: %s", joined)
	}
}

func TestStripSessionWiring(t *testing.T) {
	cat := mcpCatalog{"context7": {Command: "npx"}}
	argv := []string{
		"claude", "--resume", "abc",
		"--mcp-config", `{"mcpServers":{"partyline-context-threads":{},"context7":{}}}`, // ours → stripped
		"--append-system-prompt", "primer", // ours → stripped
		"--mcp-config", `{"mcpServers":{"users-own":{}}}`, // user's → kept
		"--permission-mode", "plan", // user's → kept
	}
	got := stripSessionWiring(argv, cat)
	want := []string{"claude", "--resume", "abc", "--mcp-config", `{"mcpServers":{"users-own":{}}}`, "--permission-mode", "plan"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("strip:\n got %q\nwant %q", got, want)
	}

	codex := []string{
		"codex",
		"-c", `mcp_servers.partyline-context-threads.command="ptln"`, // ours → stripped
		"-c", `mcp_servers.context7.args=["x"]`, // catalog-managed → stripped
		"-c", `mcp_servers.users-own.command="x"`, // not managed → kept
		"-c", `model="o3"`, // unrelated override → kept
	}
	got = stripSessionWiring(codex, cat)
	want = []string{"codex", "-c", `mcp_servers.users-own.command="x"`, "-c", `model="o3"`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codex strip:\n got %q\nwant %q", got, want)
	}
}

func TestCarryConversation(t *testing.T) {
	if got := carryConversation("claude", []string{"claude"}); !reflect.DeepEqual(got, []string{"claude", "--continue"}) {
		t.Fatalf("fresh claude should gain --continue, got %q", got)
	}
	orig := []string{"claude", "--resume", "id"}
	if got := carryConversation("claude", orig); !reflect.DeepEqual(got, orig) {
		t.Fatalf("resumed claude should be untouched, got %q", got)
	}
	if got := carryConversation("codex", []string{"codex"}); !reflect.DeepEqual(got, []string{"codex"}) {
		t.Fatalf("codex should be untouched, got %q", got)
	}
}
