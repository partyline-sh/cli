package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTasks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "backlog.txt")
	os.WriteFile(p, []byte("# a comment\n\nfirst task\n  second task  \n# skip\nthird\n"), 0o644)
	tasks, err := parseTasks(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first task", "second task", "third"}
	if len(tasks) != 3 || tasks[0] != want[0] || tasks[1] != want[1] || tasks[2] != want[2] {
		t.Fatalf("got %v want %v", tasks, want)
	}
}

func TestCrankShouldHalt(t *testing.T) {
	// item cap
	if halt, _ := crankShouldHalt(3, 0, crankOpts{max: 3}); !halt {
		t.Fatal("should halt at the item cap")
	}
	if halt, _ := crankShouldHalt(2, 0, crankOpts{max: 3}); halt {
		t.Fatal("should NOT halt before the cap")
	}
	// consecutive failures
	if halt, why := crankShouldHalt(5, 2, crankOpts{haltOnFail: 2}); !halt || why == "" {
		t.Fatalf("should halt on 2 consecutive fails: halt=%v why=%q", halt, why)
	}
	if halt, _ := crankShouldHalt(5, 1, crankOpts{haltOnFail: 2}); halt {
		t.Fatal("one failure should not halt")
	}
	// max=0 means unlimited
	if halt, _ := crankShouldHalt(100, 0, crankOpts{max: 0, haltOnFail: 2}); halt {
		t.Fatal("max=0 must not cap")
	}
}

func TestFirstWords(t *testing.T) {
	if got := firstWords("add a dark mode toggle to the navbar", 4); got != "add a dark mode" {
		t.Fatalf("got %q", got)
	}
	if got := firstWords("short", 4); got != "short" {
		t.Fatalf("got %q", got)
	}
}
