package engine

import (
	"reflect"
	"testing"
)

// TestPartyArgs pins the party-turn argv builders to the exact output the old
// party_agent.go engines map produced — the migration must be byte-identical.
func TestPartyArgs(t *testing.T) {
	extra := []string{"--permission-mode", "bypassPermissions"}
	tests := []struct {
		engine string
		model  string
		extra  []string
		want   []string
		bin    string
		stdin  bool
		stream bool
	}{
		{"claude", "", nil, []string{"-p", "--output-format", "stream-json", "--verbose"}, "claude", true, true},
		{"claude", "haiku", extra, []string{"-p", "--output-format", "stream-json", "--verbose", "--model", "haiku", "--permission-mode", "bypassPermissions"}, "claude", true, true},
		{"codex", "", nil, []string{"exec"}, "codex", true, false},
		{"codex", "o3", extra, []string{"exec", "-m", "o3", "--permission-mode", "bypassPermissions"}, "codex", true, false},
		{"gemini", "", nil, []string{"-p"}, "gemini", false, false},
		{"gemini", "gemini-2.5-pro", extra, []string{"-m", "gemini-2.5-pro", "--permission-mode", "bypassPermissions", "-p"}, "gemini", false, false},
		{"antigravity", "", nil, []string{"-p"}, "agy", false, false},
		{"antigravity", "gemini-3-pro", extra, []string{"--model", "gemini-3-pro", "--permission-mode", "bypassPermissions", "-p"}, "agy", false, false},
	}
	for _, tt := range tests {
		spec, ok := Lookup(tt.engine)
		if !ok {
			t.Fatalf("Lookup(%q) not found", tt.engine)
		}
		if spec.Bin != tt.bin || spec.Stdin != tt.stdin || spec.Stream != tt.stream {
			t.Errorf("%s: bin/stdin/stream = %s/%v/%v, want %s/%v/%v",
				tt.engine, spec.Bin, spec.Stdin, spec.Stream, tt.bin, tt.stdin, tt.stream)
		}
		if got := spec.Args(tt.model, tt.extra); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s.Args(%q, %v) = %q, want %q", tt.engine, tt.model, tt.extra, got, tt.want)
		}
	}
}

func TestLookupValid(t *testing.T) {
	for _, name := range Names() {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false, want true", name)
		}
		if s, ok := Lookup(name); !ok || s.Name != name {
			t.Errorf("Lookup(%q) = (%q, %v), want (%q, true)", name, s.Name, ok, name)
		}
	}
	for _, name := range []string{"", "agy", "gpt", "CLAUDE"} {
		if Valid(name) {
			t.Errorf("Valid(%q) = true, want false", name)
		}
		if _, ok := Lookup(name); ok {
			t.Errorf("Lookup(%q) found, want miss", name)
		}
	}
}

func TestLabel(t *testing.T) {
	if got := Label(""); got != "claude" {
		t.Errorf("Label(\"\") = %q, want \"claude\"", got)
	}
	if got := Label("codex"); got != "codex" {
		t.Errorf("Label(\"codex\") = %q, want \"codex\"", got)
	}
}

func TestNames(t *testing.T) {
	want := []string{"claude", "codex", "gemini", "opencode", "antigravity"}
	if got := Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if len(specs) != len(want) {
		t.Errorf("specs has %d entries, Names() lists %d — keep them in sync", len(specs), len(want))
	}
}

// TestCaps pins the capability matrix (the values gate real code paths — see
// the Caps field comments for file:line references).
func TestCaps(t *testing.T) {
	want := map[string]Caps{
		"claude":      {Resume: true, Stream: true, Vision: true, MCPPerInvocation: true},
		"codex":       {MCPPerInvocation: true},
		"gemini":      {},
		"antigravity": {},
	}
	for name, w := range want {
		s, _ := Lookup(name)
		if s.Caps != w {
			t.Errorf("%s.Caps = %+v, want %+v", name, s.Caps, w)
		}
	}
}
