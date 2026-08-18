// Package surfacegen turns the declarations in internal/surface and internal/clispec, plus the
// extraction in internal/surfacescan, into the artifacts that used to be written by hand.
//
// WHY THIS EXISTS. Every artifact below is a restatement of something the code already knows, and
// each restatement has its own decay rate. The evidence is on disk: web/public/llms.txt was last
// touched on 5 July and still describes a three-feature product; the run-preset allowlist is
// duplicated between TypeScript and a Postgres CHECK constraint, and getting them out of step
// makes the endpoint 500 (constraint #195).
//
// The rule this package enforces: a file listed in Files() is OUTPUT. Editing it by hand is a CI
// failure, not a merge conflict waiting to happen — see Check(), wired into `make surface-check`.
//
// A generated artifact that is WRONG is worse than none, because it carries the authority of
// having been produced by a machine. So generation is deterministic (sorted input, no timestamps,
// no host paths), and every file carries a header saying where it came from and how to regenerate.
package surfacegen

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"partyline.sh/partyline/internal/features"
	"partyline.sh/partyline/internal/surfacescan"
)

// File is one generated artifact: a repo-relative path and its full contents.
type File struct {
	Path string
	Body []byte
}

// Files produces every artifact for the repository at root. Deterministic: the same tree always
// produces the same bytes, which is what lets Check() be a diff.
func Files(root string) ([]File, error) {
	s, err := surfacescan.Scan(root)
	if err != nil {
		return nil, err
	}
	// A malformed features.json stops generation dead rather than emitting an env.example with no
	// features in it — a deploy file that silently loses every integration is the worst possible
	// artifact to produce quietly.
	reg, err := features.Load(root)
	if err != nil {
		return nil, err
	}
	// The llms files carry hand-written prose alongside their generated lists, so generation can
	// fail: a sentence naming a page that no longer exists stops the build rather than shipping a
	// confident lie to the one audience that cannot check.
	short, err := genLLMsShort(s)
	if err != nil {
		return nil, err
	}
	full, err := genLLMsFull(root, s, reg)
	if err != nil {
		return nil, err
	}
	files := []File{
		{Path: "deploy/stack/env.example", Body: genEnvExample(reg)},
		{Path: "web/src/lib/generated/vocab.ts", Body: genTypeScript()},
		{Path: "docs/reference/vocabularies.md", Body: genVocabReference()},
		{Path: "docs/reference/cli.md", Body: genCLIReference()},
		{Path: "docs/reference/api.md", Body: genAPIReference(s)},
		{Path: "docs/reference/schema.md", Body: genSchemaReference(s)},
		{Path: "docs/reference/environment.md", Body: genEnvReference(s)},
		{Path: "web/public/llms.txt", Body: short},
		{Path: "web/public/llms-full.txt", Body: full},
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Write emits every artifact, creating directories as needed. Returns the paths it changed, so a
// human running `make surface-gen` sees what moved rather than a silent success.
func Write(root string) ([]string, error) {
	files, err := Files(root)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, f := range files {
		abs := filepath.Join(root, filepath.FromSlash(f.Path))
		if old, err := os.ReadFile(abs); err == nil && bytes.Equal(old, f.Body) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return changed, err
		}
		if err := os.WriteFile(abs, f.Body, 0o644); err != nil {
			return changed, err
		}
		changed = append(changed, f.Path)
	}
	return changed, nil
}

// Check reports which generated files on disk disagree with what the declarations produce. This is
// the drift gate: it is the difference between "the reference is generated" as an aspiration and
// as a property CI enforces.
func Check(root string) ([]string, error) {
	files, err := Files(root)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, f := range files {
		old, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		if err != nil || !bytes.Equal(old, f.Body) {
			stale = append(stale, f.Path)
		}
	}
	return stale, nil
}

// header returns the do-not-edit banner in the given comment syntax. Every generated file starts
// with one: a reader who does not know this package exists still learns not to edit the file, and
// learns the one command that regenerates it.
func header(commentPrefix string) string {
	var b bytes.Buffer
	for _, line := range []string{
		"GENERATED — DO NOT EDIT.",
		"",
		"Source: internal/surface (vocabularies), internal/clispec (commands),",
		"        internal/surfacescan (routes, schema, environment).",
		"Regenerate: make surface-gen     Verify: make surface-check",
		"",
		"Hand edits are reverted by the next regeneration and fail CI in the meantime.",
		"To change what this says, change the declaration it is generated from.",
	} {
		if line == "" {
			fmt.Fprintf(&b, "%s\n", trimRight(commentPrefix))
			continue
		}
		fmt.Fprintf(&b, "%s %s\n", commentPrefix, line)
	}
	return b.String()
}

func trimRight(s string) string {
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}
