package main

import (
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// Backend selection: on when tmux exists, off on PARTYLINE_MUX=classic. This one env var is
// the whole escape hatch back to the built-in mux — it must actually work.
func TestUseTmuxBackend(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_MUX", "")
	if !useTmuxBackend() {
		t.Error("a usable tmux is the default backend now")
	}
	t.Setenv("PARTYLINE_MUX", "classic")
	if useTmuxBackend() {
		t.Error("PARTYLINE_MUX=classic must restore the built-in mux")
	}
	for v, want := range map[string]bool{"tmux 3.7c": true, "tmux 3.3a": true, "tmux 3.2a": false, "tmux next-3.6": true, "garbage": false} {
		if tmuxVersionOK(v) != want {
			t.Errorf("tmuxVersionOK(%q) = %v, want %v", v, !want, want)
		}
	}
}

// insidePtlnTmux must recognize only OUR server — creating windows in a user's personal
// tmux session would be the graduation's worst regression.
func TestInsidePtlnTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	if insidePtlnTmux() {
		t.Error("no TMUX → not inside")
	}
	t.Setenv("TMUX", "/tmp/tmux-501/default,1234,0")
	if insidePtlnTmux() {
		t.Error("a personal tmux server must not count as ours")
	}
	t.Setenv("TMUX", "/tmp/tmux-501/partyline,1234,0")
	if !insidePtlnTmux() {
		t.Error("the partyline socket is ours")
	}
}

// Window names end up inside status-format strings — '#' would be interpreted as a tmux
// format spec and control chars would wreck the bar this backend exists to keep clean.
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

// The conf carries the UX promises: ctrl-\ prefix, chords that call back into ptln (so new
// windows are tagged for workspace save), the save hooks, and the brand colors.
func TestTmuxConfContent(t *testing.T) {
	conf := tmuxConf()
	for _, want := range []string{
		"set -g prefix None",    // no invisible prefix state — ctrl-\ opens THE menu
		"tmux --menu",           // the one control surface
		"tmux --save-workspace", // window close snapshots the workspace for --resume
		"tmux --detached",       // detach snapshots AND fires scribe (the mux quit hook, ported)
		"client-detached",
		"window-unlinked",
		brand.Hex(brand.AmberRGB), // wordmark amber
		brand.Hex(brand.PillRGB),  // focused pill
		"GENERATED",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q", want)
		}
	}
	// the ribbon is a plain tab bar again — no chord panel, no armed-state flip
	for _, gone := range []string{"client_prefix", "status-format[0]", "bind n ", "bind Left"} {
		if strings.Contains(conf, gone) {
			t.Errorf("conf still carries %q — the two-surface UI is supposed to be gone", gone)
		}
	}
}

// Live round trip on a scratch socket: merge creates tagged windows, a second merge reuses
// them by key instead of duplicating, and --save-workspace's reader recovers the exact
// specs. This is the workspace-fidelity contract the graduation stands on.
func TestTmuxMergeAndWorkspaceRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-grad")
	t.Cleanup(func() { _ = tmuxCmd("kill-server").Run() })
	_ = tmuxCmd("kill-server").Run() // a leftover server from a broken run must not leak in

	dir := t.TempDir()
	specs := []ptymux.Spec{
		{Label: "claude · payments", Key: "k1", Argv: []string{"sleep", "60"}, Dir: dir},
		{Label: "codex · web", Key: "k2", Argv: []string{"sleep", "60"}, Dir: dir},
	}
	first, err := tmuxMerge(specs)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if first == "" {
		t.Fatal("merge returned no target")
	}

	// dedupe: merging one known key + one new spec adds exactly one window
	again := []ptymux.Spec{
		{Label: "claude · payments", Key: "k1", Argv: []string{"sleep", "60"}, Dir: dir},
		{Label: "gemini · docs", Key: "k3", Argv: []string{"sleep", "60"}, Dir: dir},
	}
	target, err := tmuxMerge(again)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if target != first {
		t.Errorf("known key should resolve to its existing window (%s), got %s", first, target)
	}
	deadline := time.Now().Add(3 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		out, _ := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "x").Output()
		if n = strings.Count(string(out), "x"); n == 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n != 3 {
		t.Fatalf("want 3 windows after dedupe merge, have %d", n)
	}

	// the save reader recovers the specs, argv and keys intact
	got := tmuxWorkspaceSpecs()
	byKey := map[string]ptymux.Spec{}
	for _, sp := range got {
		byKey[sp.Key] = sp
	}
	for _, key := range []string{"k1", "k2", "k3"} {
		sp, ok := byKey[key]
		if !ok {
			t.Errorf("workspace read lost key %s (have %v)", key, len(got))
			continue
		}
		if strings.Join(sp.Argv, " ") != "sleep 60" || sp.Dir != dir {
			t.Errorf("spec %s mangled: %+v", key, sp)
		}
	}
}

// The tag format itself: what tagTmuxWindow writes is what tmuxWorkspaceSpecs parses.
func TestTmuxSpecTagEncoding(t *testing.T) {
	sp := ptymux.Spec{Label: "claude · x", Key: "abc", Argv: []string{"claude", "--resume", "abc"}, Dir: "/tmp"}
	b, _ := json.Marshal(sp)
	enc := base64.StdEncoding.EncodeToString(b)
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	var back ptymux.Spec
	if json.Unmarshal(dec, &back) != nil || back.Key != sp.Key || len(back.Argv) != 3 {
		t.Errorf("tag round trip mangled the spec: %+v", back)
	}
}

// The launcher handoff: in tmux mode a Spawn action must come back as a Suspend (the attach
// closure), never as an in-process spawn — that would resurrect the corruption path.
func TestLauncherHandoffTransformsSpawn(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("PARTYLINE_MUX", "")
	h := &llmsHome{m: &aiMenu{quickNew: true}}
	mx, err := ptymux.New(h, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.mux = mx
	act := h.HandleKey([]byte{0}) // any key; quickNew is pre-armed on the menu
	if act.Spawn != nil || len(act.SpawnMany) > 0 {
		t.Fatal("tmux mode returned an in-process spawn — the corruption path is live again")
	}
	if act.Suspend == nil {
		t.Fatal("expected the Suspend handoff closure")
	}

	t.Setenv("PARTYLINE_MUX", "classic")
	h2 := &llmsHome{m: &aiMenu{quickNew: true}, mux: mx}
	act2 := h2.HandleKey([]byte{0})
	if act2.Spawn == nil {
		t.Fatal("classic mode should spawn in-process as before")
	}
}
