package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The heartbeat is the one place where a machine's local reality is handed to the control plane, so
// it is the one place where the reference-not-command boundary is crossed by DATA. These tests pin
// what may cross: labels, presets, engines, a directory BASENAME, and now check NAMES. Not paths,
// and above all not the commands those names stand for.

func TestConfigSnapshotSendsBasenameNotPath(t *testing.T) {
	cfg := configSnapshotFrom([]daemonProject{
		{Label: "web", Path: "/Users/someone/secret-client-name/repo", Preset: "build", Engine: "claude"},
	}, "v1.2.3", "darwin")

	if len(cfg.Projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(cfg.Projects))
	}
	p := cfg.Projects[0]
	if p.DirBase != "repo" {
		t.Errorf("DirBase = %q, want the basename", p.DirBase)
	}
	if strings.Contains(p.DirBase, "/") || strings.Contains(p.DirBase, "secret-client-name") {
		t.Errorf("the absolute path leaked into the heartbeat: %q", p.DirBase)
	}
	if p.Label != "web" || p.Preset != "build" || p.Engine != "claude" {
		t.Errorf("project metadata was altered: %+v", p)
	}
	if cfg.Version != "v1.2.3" || cfg.OS != "darwin" {
		t.Errorf("version/os not carried: %+v", cfg)
	}
}

// G.6 — the settings page cannot offer a policy toggle for a check it has never heard of, so the
// NAMES have to travel. The COMMANDS must not: a repo-authored shell line sitting in the control
// plane is exactly the thing project_checks refuses to store.
func TestConfigSnapshotSendsCheckNamesButNeverCommands(t *testing.T) {
	dir := t.TempDir()
	mustWriteVerify(t, dir, `
# the file a project actually has
build: npm --prefix web run build
test:  go test ./...
gofmt -l .
curl -sf https://internal.example/secret-token-endpoint
`)

	cfg := configSnapshotFrom([]daemonProject{{Label: "app", Path: dir}}, "v1", "linux")
	got := cfg.Projects[0].Checks

	if len(got) != 2 || got[0] != "build" || got[1] != "test" {
		t.Fatalf("Checks = %v, want the two NAMED checks", got)
	}

	// The whole point, asserted against the payload rather than against my reading of the code.
	blob := jsonish(t, cfg)
	for _, secret := range []string{"npm --prefix", "go test ./...", "gofmt -l", "internal.example", dir} {
		if strings.Contains(blob, secret) {
			t.Errorf("the heartbeat carried %q — commands and paths must stay on this machine", secret)
		}
	}
}

// An auto-named check's name is derived from its command, so it moves when the command changes.
// Policy keyed on it would apply an old decision to a different command — the same reason
// applyPolicy refuses to reach one. Offering it in the picker would invite exactly that.
func TestUnnamedChecksAreNotOffered(t *testing.T) {
	dir := t.TempDir()
	mustWriteVerify(t, dir, "npm run build\ngo test ./...\n")

	cfg := configSnapshotFrom([]daemonProject{{Label: "app", Path: dir}}, "v1", "linux")
	if n := len(cfg.Projects[0].Checks); n != 0 {
		t.Errorf("Checks = %v, want none — only NAMED checks are policy-addressable", cfg.Projects[0].Checks)
	}
}

// A project with no verify file, an unreadable one, or no path at all is the common case, not an
// error case. It must beat quietly: the heartbeat carries the fleet's health and must not be the
// thing that breaks because one repo lacks a file.
func TestMissingVerifyFileBeatsQuietly(t *testing.T) {
	cfg := configSnapshotFrom([]daemonProject{
		{Label: "no-file", Path: t.TempDir()},
		{Label: "no-path", Path: ""},
	}, "v1", "linux")

	if len(cfg.Projects) != 2 {
		t.Fatalf("a project was dropped: %+v", cfg.Projects)
	}
	for _, p := range cfg.Projects {
		if len(p.Checks) != 0 {
			t.Errorf("%s: Checks = %v, want none", p.Label, p.Checks)
		}
	}
}

// A pathological verify file must not turn the heartbeat into a payload the server has to defend
// against. Bounded here, at the source, rather than trusted and trimmed later.
func TestCheckNamesAreBounded(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("c")
		b.WriteString(strings.Repeat("x", i%5))
		b.WriteString(itoaCheck(i))
		b.WriteString(": true\n")
	}
	mustWriteVerify(t, dir, b.String())

	cfg := configSnapshotFrom([]daemonProject{{Label: "app", Path: dir}}, "v1", "linux")
	if n := len(cfg.Projects[0].Checks); n > 50 {
		t.Errorf("got %d check names, want the list capped", n)
	}
}

// jsonish serialises the payload exactly as the heartbeat would, so the leak assertions test what
// crosses the wire rather than what a struct field is named.
func jsonish(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func itoaCheck(n int) string { return strconv.Itoa(n) }

func mustWriteVerify(t *testing.T, dir, body string) {
	t.Helper()
	p := filepath.Join(dir, verifyFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
