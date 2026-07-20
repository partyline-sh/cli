package engine

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const prompt = "review this diff"

// TestOneShotArgs is the golden-argv table for every engine × posture. The
// claude rows pin the EXACT flags the hardcoded call sites use today
// (verify.go, review_agent.go, describe.go, work.go); the codex/gemini rows pin
// the flags read from the installed binaries' --help.
func TestOneShotArgs(t *testing.T) {
	tests := []struct {
		name      string
		engine    string
		model     string
		posture   ToolPosture
		wantArgv  []string
		wantStdin string
		wantErr   string // substring; "" = success expected
	}{
		// claude — allowlist enforcement, prompt in argv.
		{"claude none", "claude", "", ToolsNone,
			[]string{"claude", "-p", prompt, "--output-format", "json", "--allowedTools", ""}, "", ""},
		{"claude none+model", "claude", "haiku", ToolsNone,
			[]string{"claude", "-p", prompt, "--output-format", "json", "--allowedTools", "", "--model", "haiku"}, "", ""},
		{"claude readonly", "claude", "", ToolsReadOnly,
			[]string{"claude", "-p", prompt, "--output-format", "json", "--allowedTools", "Read Grep Glob"}, "", ""},
		{"claude write", "claude", "", ToolsWrite(false),
			[]string{"claude", "-p", prompt, "--output-format", "json", "--allowedTools", "Read,Grep,Glob,Edit,Write,MultiEdit,TodoWrite"}, "", ""},
		{"claude write+bash", "claude", "sonnet", ToolsWrite(true),
			[]string{"claude", "-p", prompt, "--output-format", "json", "--allowedTools", "Read,Grep,Glob,Edit,Write,MultiEdit,TodoWrite,Bash", "--model", "sonnet"}, "", ""},

		// codex — OS sandbox enforcement, prompt on stdin.
		{"codex none", "codex", "", ToolsNone, nil, "", "ToolsNone"},
		{"codex readonly", "codex", "", ToolsReadOnly,
			[]string{"codex", "exec", "--sandbox", "read-only"}, prompt, ""},
		{"codex readonly+model", "codex", "o3", ToolsReadOnly,
			[]string{"codex", "exec", "-m", "o3", "--sandbox", "read-only"}, prompt, ""},
		{"codex write no bash", "codex", "", ToolsWrite(false), nil, "", "allowBash=false"},
		{"codex write+bash", "codex", "", ToolsWrite(true),
			[]string{"codex", "exec", "--sandbox", "workspace-write"}, prompt, ""},

		// gemini — approval-mode enforcement, prompt as -p value.
		{"gemini none", "gemini", "", ToolsNone, nil, "", "ToolsNone"},
		{"gemini readonly", "gemini", "", ToolsReadOnly,
			[]string{"gemini", "--approval-mode", "plan", "-p", prompt}, "", ""},
		{"gemini write no bash", "gemini", "gemini-2.5-pro", ToolsWrite(false),
			[]string{"gemini", "-m", "gemini-2.5-pro", "--approval-mode", "auto_edit", "-p", prompt}, "", ""},
		{"gemini write+bash", "gemini", "", ToolsWrite(true),
			[]string{"gemini", "--approval-mode", "yolo", "-p", prompt}, "", ""},

		// antigravity — no enforceable posture without a full bypass: always refuse.
		{"antigravity none", "antigravity", "", ToolsNone, nil, "", "antigravity"},
		{"antigravity readonly", "antigravity", "", ToolsReadOnly, nil, "", "antigravity"},
		{"antigravity write", "antigravity", "", ToolsWrite(true), nil, "", "antigravity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := Lookup(tt.engine)
			if !ok {
				t.Fatalf("Lookup(%q) not found", tt.engine)
			}
			argv, stdin, err := spec.OneShotArgs(prompt, tt.model, tt.posture)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got argv %q", tt.wantErr, argv)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(argv, tt.wantArgv) {
				t.Errorf("argv = %q, want %q", argv, tt.wantArgv)
			}
			if stdin != tt.wantStdin {
				t.Errorf("stdinPrompt = %q, want %q", stdin, tt.wantStdin)
			}
			// The prompt must be delivered exactly once: in argv XOR on stdin.
			inArgv := false
			for _, a := range argv {
				if a == prompt {
					inArgv = true
				}
			}
			if inArgv == (stdin != "") {
				t.Errorf("prompt delivered in argv=%v AND stdin=%q — must be exactly one", inArgv, stdin)
			}
		})
	}
}

func TestToolPostureString(t *testing.T) {
	for got, want := range map[string]string{
		ToolsNone.String():        "ToolsNone",
		ToolsReadOnly.String():    "ToolsReadOnly",
		ToolsWrite(true).String(): "ToolsWrite(allowBash=true)",
	} {
		if got != want {
			t.Errorf("posture String() = %q, want %q", got, want)
		}
	}
}

// opencode: every posture is enforceable (the first non-claude engine with no refused posture) —
// but the enforcement is NOT in argv: it rides in OPENCODE_CONFIG_CONTENT (OneShotEnv), verified
// against opencode 1.18.4. These rows pin both halves: the argv stays posture-free, and the env
// carries a deny-by-default permission block that only opens what the posture grants.
func TestOpencodeOneShot(t *testing.T) {
	spec, ok := Lookup("opencode")
	if !ok {
		t.Fatal("opencode spec missing")
	}
	prompt := "review this"
	for _, tc := range []struct {
		name    string
		model   string
		posture ToolPosture
		want    []string
		env     string
	}{
		{"none", "", ToolsNone,
			[]string{"opencode", "run", prompt},
			`OPENCODE_CONFIG_CONTENT={"permission":{"*":"deny"}}`},
		{"readonly+model", "moonshot/kimi-k3", ToolsReadOnly,
			[]string{"opencode", "run", "-m", "moonshot/kimi-k3", prompt},
			`OPENCODE_CONFIG_CONTENT={"permission":{"*":"deny","read":"allow","grep":"allow","glob":"allow"}}`},
		{"write no bash", "", ToolsWrite(false),
			[]string{"opencode", "run", prompt},
			`OPENCODE_CONFIG_CONTENT={"permission":{"*":"deny","read":"allow","edit":"allow","grep":"allow","glob":"allow"}}`},
		{"write with bash", "ollama/qwen3", ToolsWrite(true),
			[]string{"opencode", "run", "-m", "ollama/qwen3", prompt},
			`OPENCODE_CONFIG_CONTENT={"permission":{"*":"deny","read":"allow","edit":"allow","grep":"allow","glob":"allow","bash":"allow"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv, stdin, err := spec.OneShotArgs(prompt, tc.model, tc.posture)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if stdin != "" {
				t.Fatalf("prompt must be the final positional, not stdin (got stdin %q)", stdin)
			}
			if got := fmt.Sprintf("%v", argv); got != fmt.Sprintf("%v", tc.want) {
				t.Fatalf("argv = %v, want %v", argv, tc.want)
			}
			env := spec.OneShotEnv(tc.posture)
			if len(env) != 1 || env[0] != tc.env {
				t.Fatalf("env = %v, want [%s]", env, tc.env)
			}
		})
	}
	// Non-opencode engines carry no posture env — the argv IS the enforcement there.
	if claude, _ := Lookup("claude"); claude.OneShotEnv(ToolsNone) != nil {
		t.Fatal("claude must not emit posture env")
	}
}
