package main

import (
	"strings"
	"testing"
)

// The fleet-map heartbeat (#267) must NEVER leak an absolute path (or anything secret) — the
// snapshot carries the dir BASENAME only. This pins that invariant: given projects with real
// absolute paths, no DirBase contains a path separator or the leading directories.
func TestConfigSnapshotNeverLeaksPaths(t *testing.T) {
	projects := []daemonProject{
		{Label: "monolith", Preset: "spec", Engine: "claude", Path: "/Users/darcy/dev/secret-monolith"},
		{Label: "aggregator", Preset: "build", Path: "/home/ci/private/acr-cloud-aggregator"},
	}
	snap := configSnapshotFrom(projects, "1.2.3", "darwin")

	if snap.Version != "1.2.3" || snap.OS != "darwin" {
		t.Fatalf("version/os passthrough wrong: %+v", snap)
	}
	if len(snap.Projects) != len(projects) {
		t.Fatalf("expected %d projects, got %d", len(projects), len(snap.Projects))
	}
	for i, p := range snap.Projects {
		if strings.ContainsAny(p.DirBase, "/\\") {
			t.Errorf("project %d DirBase %q contains a path separator — absolute path leaked", i, p.DirBase)
		}
		if strings.Contains(p.DirBase, "Users") || strings.Contains(p.DirBase, "home") {
			t.Errorf("project %d DirBase %q looks like it carries parent dirs", i, p.DirBase)
		}
	}
	if snap.Projects[0].DirBase != "secret-monolith" || snap.Projects[1].DirBase != "acr-cloud-aggregator" {
		t.Errorf("basenames wrong: %q, %q", snap.Projects[0].DirBase, snap.Projects[1].DirBase)
	}
	// Label/preset/engine still carried (metadata the fleet map needs).
	if snap.Projects[0].Label != "monolith" || snap.Projects[0].Engine != "claude" {
		t.Errorf("metadata dropped: %+v", snap.Projects[0])
	}
}
