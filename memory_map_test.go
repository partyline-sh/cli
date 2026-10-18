package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mapFact(kind, body string, tags ...string) fact {
	return fact{ID: newFactID(time.Now()), Kind: kind, Body: body, At: time.Now(), Tags: tags}
}

// The whole point of the map is that a checkout carries its context to whatever engine the next
// person runs. If the facts do not reach the file, nothing else in this file matters.
func TestMapPutsFactsInTheFileEveryEngineReads(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# House rules\n\nWe squash merge.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []fact{
		mapFact("contract", "The event stream uses ONE schema wrapped in CloudEvents 1.0 JSON, retained and replayable for 90 days."),
		mapFact("gotcha", "Xero's /Reports/TrialBalance ignores the date parameter and returns activity to date."),
	} {
		if _, err := writeFact(repo, f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writeMemoryMap(repo); err != nil {
		t.Fatal(err)
	}

	got := readFile(t, filepath.Join(repo, "AGENTS.md"))
	for _, want := range []string{"CloudEvents 1.0 JSON", "TrialBalance", "Interface contracts", "Gotchas"} {
		if !strings.Contains(got, want) {
			t.Errorf("map is missing %q:\n%s", want, got)
		}
	}
	// The human's own prose has to survive, or nobody will keep the tool installed.
	if !strings.Contains(got, "We squash merge.") {
		t.Errorf("the hand-written content was clobbered:\n%s", got)
	}
}

// Re-rendering must replace the block, not stack copies of it. This is the failure that makes a
// managed block worse than no block: three releases later the file is mostly markers.
func TestMapIsUpsertedNotAppended(t *testing.T) {
	repo := t.TempDir()
	writeOrFail(t, repo, mapFact("constraint", "There is no SQL database on a store server; the POS stores data in ISAM files."))
	for i := 0; i < 3; i++ {
		if _, err := writeMemoryMap(repo); err != nil {
			t.Fatal(err)
		}
	}
	got := readFile(t, filepath.Join(repo, "AGENTS.md"))
	if n := strings.Count(got, mapBegin); n != 1 {
		t.Fatalf("expected exactly 1 managed block, found %d", n)
	}
}

// Creating three files in someone's repository because they recorded one fact is not a reasonable
// thing for a tool to do unasked.
func TestMapOnlyCreatesOneFileWhenTheRepoHasNone(t *testing.T) {
	repo := t.TempDir()
	writeOrFail(t, repo, mapFact("decision", "Sales rollups are stored by UTC hour and rendered in each viewer's own time zone."))
	if _, err := writeMemoryMap(repo); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(repo, "AGENTS.md")) {
		t.Error("AGENTS.md was not created, so the map reaches no engine")
	}
	for _, name := range []string{"CLAUDE.md", "GEMINI.md"} {
		if fileExists(filepath.Join(repo, name)) {
			t.Errorf("%s was created unasked", name)
		}
	}
}

// ...but a repo that already keeps those files gets all of them updated.
func TestMapUpdatesEveryEngineFileTheRepoAlreadyKeeps(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeOrFail(t, repo, mapFact("constraint", "Never put /u/acr/obj on a global library path; its vendored OpenSSL shadows the host's."))
	if _, err := writeMemoryMap(repo); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md"} {
		if !strings.Contains(readFile(t, filepath.Join(repo, name)), "/u/acr/obj") {
			t.Errorf("%s did not get the map", name)
		}
	}
}

// A seeded legacy repo can emit hundreds of facts, and every one would land in every teammate's
// prompt. When the cap bites, the thing a dependent component relies on must outrank a note that
// something is unknown.
func TestMapCapDropsQuestionsBeforeContracts(t *testing.T) {
	var facts []fact
	for i := 0; i < 400; i++ {
		facts = append(facts, mapFact("question", strings.Repeat("is this still true, nobody has checked ", 3)))
	}
	keep := mapFact("contract", "THE LOAD BEARING CONTRACT: callers depend on this exact shape and it must not change.")
	facts = append(facts, keep)

	out := renderMemoryMap("repo", facts)
	if len(out) == 0 {
		t.Fatal("rendered nothing")
	}
	if !strings.Contains(out, "THE LOAD BEARING CONTRACT") {
		t.Error("the contract was dropped while questions survived — the drop order is backwards")
	}
	if !strings.Contains(out, "not shown") {
		t.Error("facts were dropped without telling the reader any were dropped")
	}
}

// Sections are sourced by tag before kind, so a repo's purpose and its seams do not scatter
// through Constraints and Decisions.
func TestTaggedFactsLandInTheirOwnSection(t *testing.T) {
	out := renderMemoryMap("repo", []fact{
		mapFact("constraint", "This service reconciles bank feeds against the ledger nightly.", "overview"),
		mapFact("decision", "The POS talks to the cloud over a single outbound tunnel; nothing inbound.", "architecture"),
	})
	whatIdx := strings.Index(out, "reconciles bank feeds")
	archIdx := strings.Index(out, "single outbound tunnel")
	if whatIdx < 0 || archIdx < 0 {
		t.Fatalf("facts missing from map:\n%s", out)
	}
	if whatIdx > strings.Index(out, "### Architecture and seams") {
		t.Error("the overview-tagged fact did not land in 'What this is'")
	}
	if archIdx > strings.Index(out, "### Interface contracts") {
		t.Error("the architecture-tagged fact did not land in 'Architecture and seams'")
	}
}

// An empty section is left in deliberately: a gap tells a reader nobody has recorded this, which
// is different from the section not existing.
func TestEmptySectionsReadAsGaps(t *testing.T) {
	out := renderMemoryMap("repo", []fact{mapFact("gotcha", "The build is not reproducible on macOS 26 without a clean module cache.")})
	if !strings.Contains(out, "### Open questions") {
		t.Error("an empty section was omitted instead of showing as a gap")
	}
	if !strings.Contains(out, "none open.") {
		t.Error("the empty section says nothing about being empty")
	}
}

// Deleting the last fact must not leave behind a file partyline created and never cleans up.
func TestRemovingTheLastFactRemovesAFileWeCreated(t *testing.T) {
	repo := t.TempDir()
	f := mapFact("gotcha", "The tray binary needs cgo, which is why it is a separate goreleaser build.")
	writeOrFail(t, repo, f)
	if _, err := writeMemoryMap(repo); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(repo, "AGENTS.md")) {
		t.Fatal("setup failed: no AGENTS.md")
	}
	if _, err := removeFact(repo, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := writeMemoryMap(repo); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(repo, "AGENTS.md")) {
		t.Error("AGENTS.md survived with nothing in it")
	}
}

// The same, but where the human had their own content: the block goes, the file stays.
func TestStrippingKeepsAFileTheHumanOwns(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# House rules\n\nAlways rebase.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := mapFact("gotcha", "The tray binary needs cgo, which is why it is a separate goreleaser build.")
	writeOrFail(t, repo, f)
	if _, err := writeMemoryMap(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := removeFact(repo, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := writeMemoryMap(repo); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "Always rebase.") {
		t.Error("the human's file was deleted along with the block")
	}
	if strings.Contains(got, mapBegin) {
		t.Error("the managed block was left behind")
	}
}

func writeOrFail(t *testing.T, repo string, f fact) {
	t.Helper()
	if _, err := writeFact(repo, f); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The map tells the reader to correct a fact with `ptln memory edit <id>`, so the id it prints has
// to resolve to exactly one fact. A short prefix does not: ids begin with the date, so every fact
// recorded on the same day rendered as the identical string and the instruction was unusable.
func TestMapPrintsAnIdThatEditCanResolve(t *testing.T) {
	repo := t.TempDir()
	var ids []string
	for i := 0; i < 3; i++ {
		f := mapFact("gotcha", "A distinct durable fact number "+string(rune('a'+i))+" with enough body to pass the gate.")
		writeOrFail(t, repo, f)
		ids = append(ids, f.ID)
	}
	out := renderMemoryMap("repo", mustFacts(t, repo))
	for _, id := range ids {
		if !strings.Contains(out, id) {
			t.Fatalf("map does not print the full id %s:\n%s", id, out)
		}
		if _, err := findFact(repo, id); err != nil {
			t.Errorf("the id the map prints does not resolve: %v", err)
		}
	}
}

func mustFacts(t *testing.T, repo string) []fact {
	t.Helper()
	f, err := readFacts(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
