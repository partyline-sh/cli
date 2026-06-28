package main

import (
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
