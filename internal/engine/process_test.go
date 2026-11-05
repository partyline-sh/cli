package engine

import "testing"

func TestProcessIdentityUsesExecutableOrInstalledWrapper(t *testing.T) {
	for _, tt := range []struct {
		argv []string
		want string
	}{
		{[]string{"/usr/bin/claude.exe"}, "claude"},
		{[]string{"node", "/opt/homebrew/bin/codex"}, "codex"},
		{[]string{"node", "/opt/lib/node_modules/@openai/codex/bin/codex.js"}, "codex"},
		{[]string{"node", "--require", "hook.js", "/opt/lib/node_modules/@google/gemini-cli/dist/index.js"}, "gemini"},
		{[]string{"agy"}, "antigravity"},
		{[]string{"goose"}, "goose"},
		{[]string{"llm", "chat"}, "llm"},
		{[]string{"node", "/srv/app.js", "claude", "codex"}, ""},
		{[]string{"node", "/srv/codex-proxy.js"}, ""},
		{[]string{"node", "-e", "claude"}, ""},
		{[]string{"node", "--eval=codex"}, ""},
		{[]string{"codex", "mcp-server"}, ""},
		{[]string{"claude", "mcp", "serve"}, ""},
		{[]string{"zsh", "-c", "claude"}, ""},
		{[]string{"vim", "claude"}, ""},
		{[]string{"claude", "-p", "hello"}, ""},
		{[]string{"codex", "exec", "hello"}, ""},
		{[]string{"opencode", "run", "hello"}, ""},
	} {
		got, ok := Process(tt.argv)
		if ok != (tt.want != "") || got.Name != tt.want {
			t.Errorf("Process(%q)=%q,%v want %q", tt.argv, got.Name, ok, tt.want)
		}
	}
}
