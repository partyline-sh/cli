package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolveLaunch is the security chokepoint: a reference (a label) only becomes a command by
// EXACT match against the local registry. These tests pin that — injection/unknown labels must
// fail to resolve, and a valid one yields a fixed argv in the registered dir.
func TestResolveLaunch(t *testing.T) {
	tmp := t.TempDir() // a real dir so the existence check passes for valid cases
	reg := daemonRegistry{Projects: []daemonProject{
		{Label: "project-a", Path: tmp, Preset: "spec"},
		{Label: "casual", Path: tmp, Preset: "chat"},
	}}
	link := "https://partyline.sh/p/abc#t=plt_pty_x"

	// unknown / injection labels never resolve (exact-match only)
	for _, bad := range []string{"nope", "../../etc", "project-a/../casual", "PROJECT-A", ""} {
		if _, _, err := resolveLaunch(reg, launchRef{ProjectLabel: bad, PartyLink: link}); err == nil {
			t.Errorf("expected error for label %q, got none", bad)
		}
	}

	// a non-link party reference is rejected
	if _, _, err := resolveLaunch(reg, launchRef{ProjectLabel: "project-a", PartyLink: "rm -rf /"}); err == nil {
		t.Error("expected error for non-http party link")
	}

	// valid spec project → grounded (--evidence), read-only tools, in the registered dir
	argv, dir, err := resolveLaunch(reg, launchRef{ProjectLabel: "project-a", PartyLink: link})
	if err != nil {
		t.Fatalf("valid resolve failed: %v", err)
	}
	if dir != tmp {
		t.Errorf("dir = %q, want %q", dir, tmp)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"party", link, "--name project-a", "--evidence", "--allowedTools Read,Grep,Glob"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q (got: %s)", want, joined)
		}
	}

	// chat preset → no --evidence (not grounded)
	argv, _, err = resolveLaunch(reg, launchRef{ProjectLabel: "casual", PartyLink: link})
	if err != nil {
		t.Fatalf("chat resolve failed: %v", err)
	}
	if strings.Contains(strings.Join(argv, " "), "--evidence") {
		t.Error("chat preset should not be grounded")
	}
}

// TestCycleJoinable pins the S2 [P] availability cycle: a project advances
// off → joinable(ask) → joinable(auto) → off, persisting to the local registry, and a
// basename-label collision (two dirs, same basename) is refused rather than silently
// clobbering the existing label→path mapping. Isolated via $HOME so it never touches the
// real ~/.partyline registry.
func TestCycleJoinable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()

	// off → ask
	if st, _ := cycleJoinable(dir); st != "ask" {
		t.Fatalf("off→ask: got %q", st)
	}
	if j := loadJoinable(); j[dir].launchPolicy() != "ask" || j[dir].Label != filepath.Base(dir) {
		t.Fatalf("registry after ask: %+v", j[dir])
	}
	// ask → auto
	if st, _ := cycleJoinable(dir); st != "auto" {
		t.Fatalf("ask→auto: got %q", st)
	}
	if loadJoinable()[dir].launchPolicy() != "auto" {
		t.Fatalf("policy not auto: %+v", loadJoinable()[dir])
	}
	// auto → off (unregistered)
	if st, _ := cycleJoinable(dir); st != "off" {
		t.Fatalf("auto→off: got %q", st)
	}
	if _, ok := loadJoinable()[dir]; ok {
		t.Fatalf("still registered after off")
	}

	// basename collision: a different dir whose basename matches an existing label is refused.
	base := filepath.Base(dir)
	other := filepath.Join(t.TempDir(), base)
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, _ := cycleJoinable(dir); st != "ask" { // re-register dir
		t.Fatalf("re-register: %q", st)
	}
	st, flash := cycleJoinable(other)
	if st != "off" || flash == "" {
		t.Fatalf("expected collision refusal, got state=%q flash=%q", st, flash)
	}
	if _, ok := loadJoinable()[other]; ok {
		t.Fatalf("collision should not have registered %q", other)
	}
}
