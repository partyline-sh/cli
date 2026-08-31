package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// The plan is where correctness lives: window names, dirs, and argv must survive the trip
// from workspace specs untouched — tmux gets the argv verbatim (no shell), so what's in
// the plan is exactly what runs.
func TestBuildTmuxLaunchFromWorkspace(t *testing.T) {
	specs := []ptymux.Spec{
		{Label: "claude · payments", Argv: []string{"claude", "--resume", "abc123", "--permission-mode", "bypassPermissions"}, Dir: "/tmp/payments"},
		{Label: "codex · web", Argv: []string{"codex", "resume", "def456"}, Dir: ""},
		{Label: "dead spec", Argv: nil, Dir: "/x"}, // unresumable — must be dropped, not crash
	}
	plan := buildTmuxLaunch("/home/d/proj", specs)
	if len(plan.windows) != 2 {
		t.Fatalf("want 2 windows (nil-argv spec dropped), got %d", len(plan.windows))
	}
	w0 := plan.windows[0]
	if w0.name != "claude · payments" || w0.dir != "/tmp/payments" {
		t.Errorf("window 0 = %q in %q", w0.name, w0.dir)
	}
	if strings.Join(w0.argv, " ") != "claude --resume abc123 --permission-mode bypassPermissions" {
		t.Errorf("argv mangled: %v", w0.argv)
	}
	// empty Dir inherits the cwd, same as the built-in mux
	if plan.windows[1].dir != "/home/d/proj" {
		t.Errorf("empty Dir should inherit cwd, got %q", plan.windows[1].dir)
	}
}

// No workspace → one fresh quick-engine window, never zero (tmux needs an initial window).
func TestBuildTmuxLaunchFresh(t *testing.T) {
	plan := buildTmuxLaunch("/home/d/proj", nil)
	if len(plan.windows) != 1 {
		t.Fatalf("want the fresh fallback window, got %d", len(plan.windows))
	}
	if plan.windows[0].argv[0] == "" || plan.windows[0].dir != "/home/d/proj" {
		t.Errorf("fresh window = %+v", plan.windows[0])
	}
}

// Window names end up inside status-format strings — '#' would be interpreted as a tmux
// format spec and control chars would wreck the bar this prototype exists to keep clean.
func TestTmuxWindowName(t *testing.T) {
	for in, want := range map[string]string{
		"claude · payments":        "claude · payments",
		"has#format":               "hasformat",
		"ctrl\x1b[2Jchars":         "ctrl[2Jchars",
		"":                         "session",
		strings.Repeat("long", 20): strings.Repeat("long", 6), // capped at 24 runes
	} {
		if got := tmuxWindowName(in); got != want {
			t.Errorf("tmuxWindowName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The conf carries the chords and the brand; these are the lines the prototype's UX
// promises depend on (ctrl-\ prefix, n/N spawn keys, amber/pill status bar).
func TestTmuxConfContent(t *testing.T) {
	conf := tmuxConf()
	for _, want := range []string{
		`set -g prefix 'C-\'`,
		"bind n new-window",
		"bind N new-window",
		brand.Hex(brand.AmberRGB), // wordmark amber
		brand.Hex(brand.PillRGB),  // focused pill
		"GENERATED",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q", want)
		}
	}
	// the N binding must carry the engine's real bypass flags, not a hardcoded claude flag
	if flags := bypassFlagsFor(quickNewEngine()); len(flags) > 0 && !strings.Contains(conf, flags[0]) {
		t.Errorf("conf's N binding lacks the engine bypass flag %q", flags[0])
	}
}

// Live round trip against a real tmux: the generated conf must parse, the plan's windows
// must open with their names and dirs, and the private socket must not leak state between
// runs. Skipped where tmux isn't installed; everything runs on a test-only socket.
func TestTmuxLiveRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := "ptln-test"
	confPath := filepath.Join(t.TempDir(), "tmux.conf")
	if err := os.WriteFile(confPath, []byte(tmuxConf()), 0o644); err != nil {
		t.Fatal(err)
	}
	tm := func(args ...string) *exec.Cmd {
		return exec.Command("tmux", append([]string{"-L", sock, "-f", confPath}, args...)...)
	}
	defer tm("kill-server").Run()

	dir := t.TempDir()
	// window 1 via new-session, window 2 via new-window — the same two paths runTmux takes
	if out, err := tm("new-session", "-d", "-s", "ptln", "-n", "claude · payments", "-c", dir, "--", "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("new-session with generated conf failed: %v\n%s", err, out)
	}
	if out, err := tm("new-window", "-t", "ptln", "-d", "-n", "codex · web", "-c", dir, "--", "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("new-window failed: %v\n%s", err, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	var names string
	for time.Now().Before(deadline) {
		out, err := tm("list-windows", "-t", "ptln", "-F", "#{window_name}").Output()
		if err == nil {
			names = string(out)
			if strings.Contains(names, "claude · payments") && strings.Contains(names, "codex · web") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(names, "claude · payments") || !strings.Contains(names, "codex · web") {
		t.Fatalf("windows not present, list-windows says:\n%s", names)
	}
	// the conf's chords actually loaded (prefix moved off C-b onto C-\)
	keys, _ := tm("list-keys", "-T", "prefix").Output()
	if !strings.Contains(string(keys), "new-window") {
		t.Errorf("prefix table missing the n/N chords:\n%.400s", keys)
	}
	prefix, _ := tm("show-options", "-g", "prefix").Output()
	if !strings.Contains(string(prefix), `C-\`) {
		t.Errorf("prefix is not ctrl-\\: %s", prefix)
	}
}
