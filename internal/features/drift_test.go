package features

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// driftRoot is a throwaway repo root containing the one doc the synthetic registries point at, so
// the anchor check has something real to find and only the drift under test is reported.
func driftRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "d.md"), []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func parseOrFail(t *testing.T, body string) Registry {
	t.Helper()
	r, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hasMention(errs []string, needle string) bool {
	for _, e := range errs {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}

// (1) The direction that catches a registry entry going stale: it names a variable no code reads.
// This is exactly what deploy/stack/env.example had done by hand for years with GITHUB_APP_CLIENT_ID.
func TestFeatureRegistryDriftDeclaredButUnread(t *testing.T) {
	in := reg{
		r:       parseOrFail(t, `{"slack": {"label": "Slack", "env": ["SLACK_CLIENT_ID", "SLACK_GHOST"], "docs": "d.md#x"}}`),
		readers: map[string][]string{"SLACK_CLIENT_ID": {"web/src/lib/api/slack-app.ts"}},
	}
	errs := driftAgainst(driftRoot(t), in)
	if !hasMention(errs, "SLACK_GHOST") {
		t.Fatalf("a declared-but-unread var was not reported as drift; got:\n  %s", joined(errs))
	}
	if hasMention(errs, "SLACK_CLIENT_ID") {
		t.Errorf("a correctly declared var was reported as drift; got:\n  %s", joined(errs))
	}
}

// (2) The direction that matters more: a new gate lands in the code and nobody declares it, so the
// doctor keeps confidently reporting yesterday's feature set.
func TestFeatureRegistryDriftUndeclaredReader(t *testing.T) {
	in := reg{
		r:          parseOrFail(t, `{"slack": {"label": "Slack", "env": ["SLACK_CLIENT_ID"], "docs": "d.md#x"}}`),
		readers:    map[string][]string{"SLACK_CLIENT_ID": {"a.ts"}, "NEW_GATE_TOKEN": {"web/src/lib/api/new.ts"}},
		classified: []NonFeature{},
	}
	errs := driftAgainst(driftRoot(t), in)
	if !hasMention(errs, "NEW_GATE_TOKEN") {
		t.Fatalf("an unaccounted-for env reader was not reported as drift; got:\n  %s", joined(errs))
	}
	// …and classifying it, with the reason written down, is the way to make the check pass.
	in.classified = []NonFeature{{Name: "NEW_GATE_TOKEN", Class: Client, Why: "dev-only"}}
	if errs := driftAgainst(driftRoot(t), in); hasMention(errs, "NEW_GATE_TOKEN") {
		t.Errorf("classifying the var did not clear the drift; got:\n  %s", joined(errs))
	}
}

// (3) and (4): a classification that outlived its variable, and a variable claimed by both tables.
func TestFeatureRegistryDriftStaleAndDoubleClaimedClassifications(t *testing.T) {
	in := reg{
		r:       parseOrFail(t, `{"slack": {"label": "Slack", "env": ["SLACK_CLIENT_ID"], "docs": "d.md#x"}}`),
		readers: map[string][]string{"SLACK_CLIENT_ID": {"a.ts"}},
		classified: []NonFeature{
			{Name: "DELETED_VAR", Class: Core, Why: "gone"},
			{Name: "SLACK_CLIENT_ID", Class: Optional, Why: "also declared — contradiction"},
			{Name: "POSTGRES_PASSWORD", Class: Compose, Why: "compose reads it; the extractor never will"},
		},
	}
	errs := driftAgainst(driftRoot(t), in)
	if !hasMention(errs, "DELETED_VAR") {
		t.Errorf("a classification for a var nothing reads was not reported; got:\n  %s", joined(errs))
	}
	if !hasMention(errs, "cannot be both") {
		t.Errorf("a var both declared and classified was not reported; got:\n  %s", joined(errs))
	}
	if hasMention(errs, "POSTGRES_PASSWORD") {
		t.Errorf("a Compose-class var was wrongly required to be code-read; got:\n  %s", joined(errs))
	}
}

// A docs anchor pointing at a file that does not exist costs the reader a whole turn — the doctor's
// only job for a not-configured feature is naming the next step.
func TestFeatureRegistryDriftPhantomDocsAnchor(t *testing.T) {
	in := reg{
		r:       parseOrFail(t, `{"slack": {"label": "Slack", "env": ["SLACK_CLIENT_ID"], "docs": "docs/nope.md#slack"}}`),
		readers: map[string][]string{"SLACK_CLIENT_ID": {"a.ts"}},
	}
	if errs := driftAgainst(driftRoot(t), in); !hasMention(errs, "docs/nope.md") {
		t.Fatalf("a docs anchor pointing nowhere was not reported; got:\n  %s", joined(errs))
	}
}

// THE RATCHET. This is the test that fails when features.json, the classification table, or the
// code they describe move apart — including when a var is added to the registry that nothing reads.
// It scans the real repository, which is the only way the check can be about reality.
func TestFeatureRegistryRepoHasNoDrift(t *testing.T) {
	root := repoRoot(t)
	r, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if errs := Drift(root, r); len(errs) > 0 {
		t.Fatalf("features.json and the code disagree (%d):\n  %s", len(errs), joined(errs))
	}
}
