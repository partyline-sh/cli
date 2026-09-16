package main

import (
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
)

// preferEngine: a VALID server engine wins; empty/unknown keeps local; notices only when
// something noteworthy happened (an override or an ignored unknown value).
func TestPreferEngine(t *testing.T) {
	cases := []struct {
		name, local, server string
		wantEngine          string
		wantNote            bool
	}{
		{"both empty", "", "", "", false},
		{"local only", "codex", "", "codex", false},
		{"server overrides empty local", "", "codex", "codex", true},
		{"server overrides different local", "gemini", "codex", "codex", true},
		{"server same as local", "codex", "codex", "codex", false},
		{"server claude over empty local — same effective engine", "", "claude", "claude", false},
		{"unknown server ignored", "codex", "not-an-engine", "codex", true},
		{"injection-shaped server ignored", "", "claude; rm -rf /", "", true},
		{"flag-shaped server ignored", "gemini", "--dangerously-bypass", "gemini", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, note := preferEngine(c.local, c.server)
			if got != c.wantEngine {
				t.Fatalf("engine = %q, want %q", got, c.wantEngine)
			}
			if (note != "") != c.wantNote {
				t.Fatalf("note = %q, wantNote=%v", note, c.wantNote)
			}
		})
	}
}

// resolveRunEngine: server-valid > the label's local registry engine > claude, always returning
// a canonical (non-empty) name.
func TestResolveRunEngine(t *testing.T) {
	reg := daemonRegistry{Projects: []daemonProject{
		{Label: "api", Path: "/tmp/api", Engine: "codex"},
		{Label: "web", Path: "/tmp/web"}, // no engine → claude
	}}
	cases := []struct {
		name, label, server, want string
	}{
		{"server wins over local", "api", "gemini", "gemini"},
		{"local when server empty", "api", "", "codex"},
		{"claude when neither", "web", "", "claude"},
		{"unknown label falls back to claude", "ghost", "", "claude"},
		{"unknown server keeps local", "api", "bogus", "codex"},
		{"injection-shaped server keeps claude default", "web", "$(reboot)", "claude"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := resolveRunEngine(reg, c.label, c.server)
			if got != c.want {
				t.Fatalf("resolveRunEngine(%q,%q) = %q, want %q", c.label, c.server, got, c.want)
			}
		})
	}
}

// reviewerOneShot: tool-less where the engine can enforce it; the ONE sanctioned downgrade to
// read-only (logged) where it can't; a hard error where neither posture is enforceable.
func TestReviewerOneShot(t *testing.T) {
	spec := func(name string) eng.Spec {
		s, ok := eng.Lookup(name)
		if !ok {
			t.Fatalf("engine %q missing from registry", name)
		}
		return s
	}
	var logged []string
	logf := func(s string) { logged = append(logged, s) }

	// claude: ToolsNone directly, no downgrade log.
	logged = nil
	argv, stdin, err := reviewerOneShot(spec("claude"), "judge this", "haiku", logf)
	if err != nil || stdin != "" {
		t.Fatalf("claude: err=%v stdin=%q", err, stdin)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "--allowedTools ") || !strings.Contains(got, "--model haiku") {
		t.Fatalf("claude argv = %q", got)
	}
	if len(logged) != 0 {
		t.Fatalf("claude should not log a downgrade: %v", logged)
	}

	// codex: downgraded to read-only sandbox, logged, prompt on stdin.
	logged = nil
	argv, stdin, err = reviewerOneShot(spec("codex"), "judge this", "", logf)
	if err != nil {
		t.Fatalf("codex: %v", err)
	}
	if stdin != "judge this" {
		t.Fatalf("codex stdin = %q", stdin)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "--sandbox read-only") {
		t.Fatalf("codex argv = %q", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "read-only") {
		t.Fatalf("codex downgrade not logged: %v", logged)
	}

	// gemini: downgraded to plan (read-only) approval mode, logged.
	logged = nil
	argv, _, err = reviewerOneShot(spec("gemini"), "judge this", "", logf)
	if err != nil {
		t.Fatalf("gemini: %v", err)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "--approval-mode plan") {
		t.Fatalf("gemini argv = %q", got)
	}
	if len(logged) != 1 {
		t.Fatalf("gemini downgrade not logged: %v", logged)
	}

	// antigravity: neither posture enforceable — hard error, nothing logged as a downgrade.
	logged = nil
	if _, _, err = reviewerOneShot(spec("antigravity"), "judge this", "", logf); err == nil {
		t.Fatal("antigravity should refuse both reviewer postures")
	}
	if len(logged) != 0 {
		t.Fatalf("antigravity must not log a downgrade it didn't take: %v", logged)
	}
}

// An unknown engine never reaches an exec — the decompose path errors immediately.
func TestRunDecomposeStreamingUnknownEngine(t *testing.T) {
	if _, err := runDecomposeStreaming(t.TempDir(), "bogus", "idea", "", "", nil, time.Second); err == nil || !strings.Contains(err.Error(), "unknown engine") {
		t.Fatalf("want unknown-engine error, got %v", err)
	}
}

// augmentRunArgv forwards ONLY registry-valid, non-claude engines to crank's argv.
func TestAugmentRunArgvEngine(t *testing.T) {
	base := []string{"crank", "--claim"}
	cases := []struct {
		name, engine string
		want         bool
	}{
		{"codex forwarded", "codex", true},
		{"gemini forwarded", "gemini", true},
		{"claude omitted (crank default)", "claude", false},
		{"empty omitted", "", false},
		{"unknown omitted", "cursor", false},
		{"injection-shaped omitted", "codex --dangerously-bypass-approvals-and-sandbox", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := augmentRunArgv(append([]string(nil), base...), api.RunEvent{RunID: "11111111-1111-1111-1111-111111111111", Engine: c.engine})
			if err != nil {
				t.Fatal(err)
			}
			has := false
			for i, a := range got {
				if a == "--engine" {
					has = true
					if i+1 >= len(got) || got[i+1] != c.engine {
						t.Fatalf("--engine value = %v", got)
					}
				}
			}
			if has != c.want {
				t.Fatalf("argv = %v, want --engine present=%v", got, c.want)
			}
		})
	}
}

// The build worker refuses what it can't enforce, before any exec: unknown engines, and
// engines whose write posture isn't enforceable headless.
func TestRunWorkerEngineRefusals(t *testing.T) {
	if _, err := runWorker(t.TempDir(), "task", "bogus", "", "", false, time.Second, nil, ""); err == nil || !strings.Contains(err.Error(), "unknown engine") {
		t.Fatalf("bogus engine: %v", err)
	}
	// codex without bash: edits ride its exec pipeline — must refuse, with an actionable message.
	if _, err := runWorker(t.TempDir(), "task", "codex", "", "", false, time.Second, nil, ""); err == nil || !strings.Contains(err.Error(), "ToolsWrite(true)") {
		t.Fatalf("codex no-bash: %v", err)
	}
	// antigravity: no enforceable headless posture at all.
	if _, err := runWorker(t.TempDir(), "task", "antigravity", "", "", true, time.Second, nil, ""); err == nil {
		t.Fatal("antigravity should be refused")
	}
}
