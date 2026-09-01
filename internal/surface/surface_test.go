package surface

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestDeclarationsAreValid(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
}

// Every vocabulary must be reachable by name — the generators address them that way, so a Vocab
// that exists as a package var but is missing from All() would silently generate nothing.
func TestAllVocabsAreLookupable(t *testing.T) {
	for _, v := range All() {
		if got, ok := Lookup(v.Name); !ok || got.Name != v.Name {
			t.Errorf("Lookup(%q) failed — is it missing from All()?", v.Name)
		}
	}
}

// An outcome we do not recognise must never be treated as retryable: an unknown code with a
// transient default would put the system into a silent retry loop on a failure nobody declared.
func TestUnknownCodeIsHard(t *testing.T) {
	if got := GateCode.ClassOf("something.nobody.declared"); got != ClassHard {
		t.Errorf("ClassOf(unknown) = %q, want %q", got, ClassHard)
	}
	if GateCode.Has("something.nobody.declared") {
		t.Error("Has() returned true for an undeclared key")
	}
}

// The rule that keeps the generated reference honest. Proven by mutation: blank any Doc below and
// this test fails.
func TestEveryTermIsDocumented(t *testing.T) {
	for _, v := range All() {
		for _, term := range v.Terms {
			if strings.TrimSpace(term.Doc) == "" {
				t.Errorf("%s.%s has no Doc", v.Name, term.Key)
			}
		}
	}
}

// ---- drift checks against the copies that already exist ----
//
// These are the whole point of the package. Until the generators land (S.4), the TypeScript
// copies are still hand-maintained, so these tests are what stop them diverging in the meantime —
// and they target the exact list that produced constraint #195.

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Skipf("cannot read %s (%v) — skipping drift check", rel, err)
	}
	return string(b)
}

// Hyphens are part of a key, not a separator. Without them this SILENTLY dropped "prime-agent"
// from the web list and reported the two sides as matching-but-shorter — a parity check that
// ignores a name is worse than no parity check, because it reads as a pass.
func quoted(s string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`"([a-z_-]+)"`).FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func assertSameSet(t *testing.T, what string, declared, found []string) {
	t.Helper()
	d, f := append([]string(nil), declared...), append([]string(nil), found...)
	sort.Strings(d)
	sort.Strings(f)
	if strings.Join(d, ",") != strings.Join(f, ",") {
		t.Errorf("%s drifted from internal/surface:\n  surface: %v\n  web:     %v", what, d, f)
	}
}

// The preset allowlist in createQueuedRun. Constraint #195: this list, the runs_preset_check
// constraint, and this package are the same list written three times.
func TestPresetMatchesWebAllowlist(t *testing.T) {
	src := repoFile(t, "web/src/lib/api/runs.ts")
	m := regexp.MustCompile(`\[((?:\s*"[a-z_]+",?)+)\]\.includes\(p\.preset`).FindStringSubmatch(src)
	if m == nil {
		t.Skip("preset allowlist not found in runs.ts — its shape changed; update this matcher")
	}
	assertSameSet(t, "preset", Preset.Keys(), quoted(m[1]))
}

// The engine set is validated server-side on three separate boundaries; all three read ENGINES.
func TestEngineMatchesWebEngines(t *testing.T) {
	src := repoFile(t, "web/src/lib/engines.ts")
	m := regexp.MustCompile(`export const ENGINES = \[([^\]]+)\]`).FindStringSubmatch(src)
	if m == nil {
		t.Skip("ENGINES not found in engines.ts — its shape changed; update this matcher")
	}
	assertSameSet(t, "engine", Engine.Keys(), quoted(m[1]))
}

// The run status vocabulary must match the CHECK constraint the database actually enforces.
// Scanned from the migration that last sets it, so this keeps working as the constraint moves.
func TestRunStatusMatchesLatestConstraint(t *testing.T) {
	assertSameSet(t, "run_status", RunStatus.Keys(), lastCheckList(t, "runs_status_check"))
}

func TestPresetMatchesLatestConstraint(t *testing.T) {
	assertSameSet(t, "preset", Preset.Keys(), lastCheckList(t, "runs_preset_check"))
}

// lastCheckList finds the newest migration that adds the named CHECK constraint and returns the
// literals in its IN list. Migrations are applied in filename order, so the last one wins.
func lastCheckList(t *testing.T, constraint string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "supabase", "migrations", "*.sql"))
	if err != nil || len(paths) == 0 {
		t.Skip("no migrations found — skipping constraint drift check")
	}
	sort.Strings(paths)
	re := regexp.MustCompile(`(?is)add\s+constraint\s+` + regexp.QuoteMeta(constraint) + `\s+check\s*\([^(]*in\s*\(([^)]*)\)`)
	var found string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if m := re.FindStringSubmatch(string(b)); m != nil {
			found = m[1]
		}
	}
	if found == "" {
		t.Skipf("constraint %s not found in migrations — update this matcher if it was renamed", constraint)
	}
	var out []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(found, -1) {
		out = append(out, m[1])
	}
	return out
}

// Constants are a convenience that can rot: a term renamed in the vocabulary leaves the constant
// pointing at a value nothing accepts, and the compiler cannot see it. This is the guard.
func TestConstantsAreDeclaredTerms(t *testing.T) {
	for _, c := range []struct {
		vocab Vocab
		val   string
	}{
		{GateVerdict, VerdictPass}, {GateVerdict, VerdictPassWithFindings}, {GateVerdict, VerdictFail},
		{GateVerdict, VerdictBlocked}, {GateVerdict, VerdictSkipped},
		{PauseReason, PauseBudget}, {PauseReason, PauseRateLimit}, {PauseReason, PauseEntitlement}, {PauseReason, PauseQuarantine}, {PauseReason, PauseStall},
		{GateCode, CodeOK}, {GateCode, CodeSkipped}, {GateCode, CodeCheckFailed}, {GateCode, CodeCheckTimeout},
		{GateCode, CodeCheckBaselineRed}, {GateCode, CodeReviewerRejected}, {GateCode, CodeReviewerUnparseable},
		{GateCode, CodeReviewerTimeout}, {GateCode, CodeReviewerNoDiff}, {GateCode, CodeVisualRejected},
		{GateCode, CodeVisualNoRenderer}, {GateCode, CodeReadOnlyMutated}, {GateCode, CodeProviderRateLimited},
		{GateCode, CodeProviderTimeout}, {GateCode, CodeProviderUnavailable}, {GateCode, CodeEngineUnknown},
		{GateCode, CodeEngineLaunchFailed},
	} {
		if !c.vocab.Has(c.val) {
			t.Errorf("constant %q is not a declared term of %s", c.val, c.vocab.Name)
		}
	}
}
