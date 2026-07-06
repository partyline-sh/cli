package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readChecks parses one command per non-empty, non-comment line from the BASE repo's verify file,
// and returns nil when there's no file (→ gate skipped, not a pass).
func TestReadChecks(t *testing.T) {
	repo := t.TempDir()
	if got := readChecks(repo); got != nil {
		t.Fatalf("no verify file → nil, got %v", got)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# acceptance checks\ngo build ./...\n\n  go test ./...  \n# trailing comment\n"
	if err := os.WriteFile(filepath.Join(repo, verifyFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readChecks(repo)
	want := []string{"go build ./...", "go test ./..."}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("readChecks = %v, want %v", got, want)
	}
}

// runChecks: no checks → skipped (ran=false); all pass → ok; the FIRST failure stops the run and
// its output is captured in the reason.
func TestRunChecks(t *testing.T) {
	wt := t.TempDir()
	timeout := 10 * time.Second

	if vr := runChecks(wt, nil, timeout); vr.ran || vr.ok {
		t.Fatalf("no checks → {ran:false}, got %+v", vr)
	}

	if vr := runChecks(wt, []string{"true", "true"}, timeout); !vr.ran || !vr.ok {
		t.Fatalf("all pass → {ran:true, ok:true}, got %+v", vr)
	}

	// The failing check's command + output must surface in the reason; a check AFTER the failure
	// must NOT run (we stop at the first failure).
	marker := filepath.Join(wt, "second-ran")
	checks := []string{
		"echo boom-output; exit 3",
		"touch " + marker,
	}
	vr := runChecks(wt, checks, timeout)
	if vr.ok || !vr.ran {
		t.Fatalf("a failing check → {ran:true, ok:false}, got %+v", vr)
	}
	if !strings.Contains(vr.reasons, "boom-output") || !strings.Contains(vr.reasons, "exit 3") {
		t.Fatalf("reason must carry the failing cmd + its output, got %q", vr.reasons)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a check after the first failure must not run")
	}
}
