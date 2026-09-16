package surfacegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The section's whole claim is that it is EXTRACTED. If a body could be authored in this package —
// or drift from the page it says it came from — the citation under each concept would be a lie, and
// a reader correcting the docs page would not change the artifact.
func TestConceptProseIsExtractedFromTheDocsPageItCites(t *testing.T) {
	cs, err := loadConcepts(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		page, rerr := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(c.Doc)))
		if rerr != nil {
			t.Errorf("%s: cannot read the page it claims to come from: %v", c.Slug, rerr)
			continue
		}
		if !strings.Contains(string(page), strings.Join(c.Body, "\n")) {
			t.Errorf("concept %q does not appear verbatim in %s — it was not extracted", c.Slug, c.Doc)
		}
	}
}

// Every required concept must actually reach llms-full.txt. Generation refuses a missing one, but
// this asserts the other half: that a concept which loads also gets rendered, with the citation
// that tells a reader which file to fix.
func TestEveryRequiredConceptReachesTheArtifact(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	var full string
	for _, f := range files {
		if f.Path == "web/public/llms-full.txt" {
			full = string(f.Body)
		}
	}
	if full == "" {
		t.Fatal("llms-full.txt was not generated")
	}
	if !strings.Contains(full, "## Concepts") {
		t.Fatal("llms-full.txt has no concepts section")
	}
	cs, err := loadConcepts(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != len(requiredConcepts) {
		t.Fatalf("loaded %d concepts, want %d", len(cs), len(requiredConcepts))
	}
	for _, c := range cs {
		if !strings.Contains(full, c.Title) {
			t.Errorf("llms-full.txt is missing concept %q (%s)", c.Slug, c.Title)
		}
		if !strings.Contains(full, c.Doc) {
			t.Errorf("concept %q is rendered without citing %s — a reader cannot tell what to fix", c.Slug, c.Doc)
		}
		if !strings.Contains(full, strings.Join(c.Body, "\n")) {
			t.Errorf("concept %q body was altered on its way into the artifact", c.Slug)
		}
	}
}

// The specific things an agent gets wrong. These are asserted as CONTENT, not as slugs, because a
// block could keep its slug and lose the sentence that makes it worth having.
func TestTheArtifactStatesTheFactsAnAgentGetsWrong(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	var full string
	for _, f := range files {
		if f.Path == "web/public/llms-full.txt" {
			full = strings.ToLower(string(f.Body))
		}
	}
	for _, want := range []string{
		"filing a plan does not start it",
		"threads.project_id",
		"thread_projects",
		"neither one writes the other",
		"reference, not command",
		"crank",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("llms-full.txt never says %q", want)
		}
	}
}

// Unstamped prose must stop generation. This is the rule that keeps the artifact from laundering
// text nobody has read against the code — the failure mode a generator makes WORSE, because
// machine-produced output is trusted more than a docs page.
func TestAnUnstampedConceptIsRefused(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "page.mdx")
	write := func(header string) []concept {
		if err := os.WriteFile(page, []byte("# x\n\n"+header+"\n\nprose.\n\n{/* endconcept */}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cs, err := parseConcepts("page.mdx", page)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 1 {
			t.Fatalf("parsed %d blocks, want 1", len(cs))
		}
		return cs
	}

	unstamped := write("{/* concept: demo\n    title: Demo\n    sources: go.mod */}")
	if err := validateConcept(dir, unstamped[0]); err == nil {
		t.Error("an unstamped concept was accepted — unverified prose can reach llms-full.txt")
	}

	// go.mod exists at the repo root; point `root` there so the source check can pass.
	stamped := write("{/* concept: demo\n    title: Demo\n    sources: go.mod\n    verified: deadbeef */}")
	if err := validateConcept(repoRoot, stamped[0]); err != nil {
		t.Errorf("a stamped concept was rejected: %v", err)
	}
	if got := strings.Join(stamped[0].Body, "\n"); got != "prose." {
		t.Errorf("body = %q, want %q", got, "prose.")
	}

	// A stamp pointing at a file that is gone is a stamp against nothing.
	phantom := write("{/* concept: demo\n    title: Demo\n    sources: no/such/file.go\n    verified: deadbeef */}")
	if err := validateConcept(repoRoot, phantom[0]); err == nil {
		t.Error("a concept citing a non-existent source was accepted")
	}
}

// Hand-editing the concepts section must fail `make surface-check`. Without this the "one place per
// explanation" rule is a convention, and the artifact becomes a second place to edit.
func TestHandEditingTheConceptsSectionIsCaught(t *testing.T) {
	const path = "web/public/llms-full.txt"
	target := filepath.Join(repoRoot, filepath.FromSlash(path))
	original, err := os.ReadFile(target)
	if err != nil {
		t.Skipf("%s not generated yet — run `make surface-gen`", path)
	}
	t.Cleanup(func() { _ = os.WriteFile(target, original, 0o644) })

	if stale, _ := Check(repoRoot); len(stale) != 0 {
		t.Fatalf("tree is already stale before the mutation: %v — run `make surface-gen`", stale)
	}
	edited := strings.Replace(string(original), "## Concepts", "## Concepts (edited by hand)", 1)
	if edited == string(original) {
		t.Fatal("could not find the concepts section to mutate")
	}
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err := Check(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0] != path {
		t.Errorf("Check() = %v, want exactly [%s] — a hand edit to the concepts section went unnoticed", stale, path)
	}
}
