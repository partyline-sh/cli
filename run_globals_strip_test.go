package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EVERY BLOCK PARTYLINE INJECTS MUST BE STRIPPED BEFORE THE COMMIT.
//
// The injected block is context FOR the worker, never part of the deliverable. When one is injected
// and not stripped it rides into every commit and every PR — reviewers graded otherwise-clean work
// down for it, and people hand-stripped it in commits titled "strip injected project-globals".
//
// Anchored context shipped injected-but-not-stripped and was worse than the original: it varies PER
// TASK by design, so two siblings in one chain wrote DIFFERENT blocks into the same tracked file and
// conflicted on content neither task authored.
//
// This reads the SOURCE so a third block cannot repeat it: any `…Begin` marker declared in
// run_globals.go must appear in the managedBlocks list the stripper walks.
func TestEveryInjectedBlockIsStripped(t *testing.T) {
	const file = "run_globals.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var markers []string
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for _, name := range vs.Names {
			if strings.HasSuffix(name.Name, "Begin") {
				markers = append(markers, name.Name)
			}
		}
		return true
	})
	if len(markers) == 0 {
		t.Fatal("found no block markers — this check would pass vacuously and protect nothing")
	}

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	list := string(src)
	start := strings.Index(list, "var managedBlocks")
	if start < 0 {
		t.Fatal("managedBlocks is gone — the stripper no longer has a single list to walk")
	}
	// The literal contains braces of its own ({begin, end} per entry), so the declaration ends at the
	// first closing brace in COLUMN ZERO, not the first closing brace.
	end := strings.Index(list[start:], "\n}")
	if end < 0 {
		t.Fatal("could not find the end of the managedBlocks declaration")
	}
	block := list[start : start+end]

	for _, m := range markers {
		if !strings.Contains(block, m) {
			t.Errorf("%s is injected but not listed in managedBlocks, so it is never stripped — it will "+
				"ride into every commit and every PR, and if its content varies per task it will conflict "+
				"between siblings in a chain on text neither task wrote.", m)
		}
	}
}

// The behavioural half: inject both blocks, strip, and the file must be back to what the repo had.
func TestStripRestoresTheRepoFile(t *testing.T) {
	wt := t.TempDir()
	own := "# The repo's own notes\n\nKeep me.\n"
	for _, name := range globalsFiles {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(own), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeWorktreeGlobals(wt, "project rules")
	writeWorktreeContext(wt, "what the team knows about these files")
	stripWorktreeGlobals(wt)

	for _, name := range globalsFiles {
		b, err := os.ReadFile(filepath.Join(wt, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := string(b)
		if !strings.Contains(got, "Keep me.") {
			t.Errorf("%s: the repo's own content was destroyed by the strip", name)
		}
		for _, marker := range []string{globalsBegin, contextBegin} {
			if strings.Contains(got, marker) {
				t.Errorf("%s: an injected block survived into the commit:\n%s", name, got)
			}
		}
	}
}

// A file the injection CREATED must be removed, not left as an empty artifact in the diff.
func TestStripRemovesAFileInjectionCreated(t *testing.T) {
	wt := t.TempDir()
	writeWorktreeGlobals(wt, "project rules")
	writeWorktreeContext(wt, "anchored context")
	stripWorktreeGlobals(wt)
	for _, name := range globalsFiles {
		if _, err := os.Stat(filepath.Join(wt, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the strip although injection created it — it lands in the PR as an empty file", name)
		}
	}
}

// Repair rounds re-inject after a commit stripped, so the pair has to survive repetition.
func TestInjectStripIsRepeatable(t *testing.T) {
	wt := t.TempDir()
	own := "# notes\n"
	path := filepath.Join(wt, globalsFiles[0])
	if err := os.WriteFile(path, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		writeWorktreeGlobals(wt, "rules")
		writeWorktreeContext(wt, "context")
		stripWorktreeGlobals(wt)
	}
	b, _ := os.ReadFile(path)
	if string(b) != own {
		t.Errorf("after 3 inject/strip rounds the file drifted:\n%q\nwant:\n%q", string(b), own)
	}
}
