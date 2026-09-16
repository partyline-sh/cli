package docsaudit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The audit's whole value is that it cannot be fooled the way a human sweep can. These pin the two
// directions and the parser's one deliberate limit.

func TestBothDirections(t *testing.T) {
	surface := []string{"cli:crank", "cli:work", "api:/api/v1/runs", "table:run_tasks"}
	claims := []Claim{
		{Doc: "docs/crank.md", Covers: []string{"cli:crank", "api:/api/v1/runs"}},
		// A doc confidently describing a command that no longer exists. This is the failure that
		// has actually happened here, twice, and that reads perfectly well as prose.
		{Doc: "docs/old.md", Covers: []string{"cli:ptln-update"}, Ordinal: 3},
	}

	r := Audit(surface, claims)

	if got := r.Undocumented; len(got) != 2 || got[0] != "cli:work" || got[1] != "table:run_tasks" {
		t.Errorf("undocumented = %v, want the two nothing claims", got)
	}
	if len(r.Phantom) != 1 || r.Phantom[0].Anchor != "cli:ptln-update" {
		t.Fatalf("phantom = %+v, want the claim naming code that is gone", r.Phantom)
	}
	if r.Phantom[0].Doc != "docs/old.md" || r.Phantom[0].Line != 3 {
		t.Errorf("phantom must name the doc AND line so it is actionable: %+v", r.Phantom[0])
	}
	if r.Claimed != 2 {
		t.Errorf("claimed = %d, want 2 — a phantom claim must not count as coverage", r.Claimed)
	}
}

// The direction that would otherwise let the audit lie in the comfortable direction: a claim for
// something absent must NOT be counted as covering anything.
func TestAPhantomClaimCoversNothing(t *testing.T) {
	r := Audit([]string{"cli:crank"}, []Claim{{Doc: "d.md", Covers: []string{"cli:gone", "cli:also-gone"}}})
	if r.Claimed != 0 {
		t.Errorf("claimed = %d, want 0", r.Claimed)
	}
	if len(r.Undocumented) != 1 {
		t.Errorf("undocumented = %v, want cli:crank still unclaimed", r.Undocumented)
	}
}

func TestDuplicateClaimsCountOnce(t *testing.T) {
	r := Audit([]string{"cli:crank"}, []Claim{
		{Doc: "a.md", Covers: []string{"cli:crank"}},
		{Doc: "b.md", Covers: []string{"cli:crank"}},
	})
	if r.Claimed != 1 || len(r.Undocumented) != 0 {
		t.Errorf("two docs covering one item = 1 covered, got claimed=%d undoc=%v", r.Claimed, r.Undocumented)
	}
}

func TestParsesEveryDocFormat(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Three formats that cannot share a parser any other way: MDX front-matter, a Markdown HTML
	// comment, and a roff comment. One line-oriented syntax works in all three.
	write("page.mdx", "---\ntitle: Crank\ncovers: [cli:crank, api:/api/v1/runs]\n---\n\n# Crank\n")
	write("guide.md", "# Guide\n\n<!-- covers: [table:run_tasks] -->\n\nProse.\n")
	write("ptln.1", ".\\\" covers: [cli:work]\n.TH PTLN 1\n")

	claims, err := ParseClaims(root, []string{"docs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 3 {
		t.Fatalf("parsed %d claims, want 3: %+v", len(claims), claims)
	}
	got := map[string]int{}
	for _, c := range claims {
		got[c.Doc] = len(c.Covers)
	}
	if got["docs/page.mdx"] != 2 || got["docs/guide.md"] != 1 || got["docs/ptln.1"] != 1 {
		t.Errorf("anchors per doc = %v", got)
	}
}

// THE DELIBERATE LIMIT. A `covers:` line deep in prose is far likelier to be an example of the
// syntax than a real declaration — and counting an example as a claim would make the audit lie in
// the direction of "everything is documented", which is the one direction it must never lie in.
func TestACoversLineBuriedInProseIsNotAClaim(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs")
	_ = os.MkdirAll(dir, 0o755)
	body := "# How to declare coverage\n" + repeat("\nprose line\n", 40) + "\ncovers: [cli:crank]\n"
	if err := os.WriteFile(filepath.Join(dir, "howto.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	claims, err := ParseClaims(root, []string{"docs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Errorf("an example deep in prose was read as a real claim: %+v", claims)
	}
}

// A doc root that does not exist yet is configuration, not an audit failure.
func TestAMissingDocRootIsNotAnError(t *testing.T) {
	if _, err := ParseClaims(t.TempDir(), []string{"does-not-exist"}); err != nil {
		t.Errorf("missing root should not fail the audit: %v", err)
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// The audit's own first run produced eight phantoms out of a task-list file that merely DESCRIBED
// the covers syntax. A prose sentence is not an anchor, and a phantom count polluted by prose is a
// number people learn to ignore — which would disarm the one check in this report that matters most.
func TestProseIsNotAnAnchor(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs")
	_ = os.MkdirAll(dir, 0o755)
	// Verbatim in shape to the line that fooled it: a real anchor next to explanatory prose.
	body := "# Backlog\n\ncovers: [cli:crank, <anchor>, and a new Go test parses the front-matter, api:]\n"
	if err := os.WriteFile(filepath.Join(dir, "backlog.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	claims, err := ParseClaims(root, []string{"docs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("claims = %+v", claims)
	}
	if got := claims[0].Covers; len(got) != 1 || got[0] != "cli:crank" {
		t.Errorf("Covers = %v, want only the well-formed anchor — prose must be dropped", got)
	}
}

// The MDX form. Trimming comment suffixes one style at a time silently dropped the LAST anchor
// here, because `*/}` stayed attached to it and it then failed validation. Taking what is between
// the brackets handles every comment style at once.
func TestMDXCommentFormKeepsEveryAnchor(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs")
	_ = os.MkdirAll(dir, 0o755)
	body := "{/* covers: [web:/runs, web:/runs/[id], web:/runs/new] */}\n\nimport X from \"y\";\n\n# Runs\n"
	if err := os.WriteFile(filepath.Join(dir, "runs.mdx"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	claims, err := ParseClaims(root, []string{"docs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("claims = %+v", claims)
	}
	want := []string{"web:/runs", "web:/runs/[id]", "web:/runs/new"}
	if got := claims[0].Covers; len(got) != 3 || got[2] != want[2] {
		t.Errorf("Covers = %v, want %v — the last anchor must survive the comment close", got, want)
	}
}

// ── D.3 staleness ─────────────────────────────────────────────────────────────

// repoRoot resolves the actual repository root: `go test` runs in the package directory, so a
// relative "." would point git at internal/docsaudit and every path lookup would silently miss.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not a git checkout")
	}
	return strings.TrimSpace(string(out))
}

// The distinction that makes this useful: "nobody has ever vouched for this" and "somebody vouched
// for it and it has not moved since" are different facts. Collapsing them is how a doc drifts for a
// year while every check stays green.
func TestUnstampedIsItsOwnState(t *testing.T) {
	claims := []Claim{{Doc: "docs/a.md", Covers: []string{"cli:crank"}}}
	src := map[string]string{"cli:crank": "crank.go"}

	// No stamp at all → every claim it makes is unverified. Uses the real repo, so LastTouched
	// resolves; crank.go has certainly been committed.
	got := CheckStaleness(repoRoot(t), claims, map[string]string{}, src)
	if len(got) != 1 || got[0].VerifiedAt != "" {
		t.Fatalf("unstamped doc = %+v, want one entry with an empty stamp", got)
	}
	if len(got[0].Moved) != 1 || got[0].Moved[0] != "cli:crank" {
		t.Errorf("Moved = %v, want the unverified anchor named", got[0].Moved)
	}
}

// A stamp at the CURRENT tip of the file cannot be stale — the source has not moved since.
func TestAStampAtTheTipIsNotStale(t *testing.T) {
	tip := LastTouched(repoRoot(t), "crank.go")
	if tip == "" {
		t.Skip("no git history for crank.go")
	}
	got := CheckStaleness(repoRoot(t),
		[]Claim{{Doc: "docs/a.md", Covers: []string{"cli:crank"}}},
		map[string]string{"docs/a.md": tip},
		map[string]string{"cli:crank": "crank.go"})
	if len(got) != 0 {
		t.Errorf("stale = %+v, want none: the stamp IS the last commit that touched the source", got)
	}
}

// An anchor with no source file (a vocabulary term) cannot go stale this way, and must not be
// reported as if it had — a false positive here is noise in the one report that has to stay quiet
// enough to read.
func TestAnAnchorWithNoSourceIsNotStale(t *testing.T) {
	got := CheckStaleness(repoRoot(t),
		[]Claim{{Doc: "docs/a.md", Covers: []string{"vocab:run_status"}}},
		map[string]string{},
		map[string]string{}) // no source mapping
	if len(got) != 0 {
		t.Errorf("stale = %+v, want none for an anchor with no source file", got)
	}
}

func TestIsAncestorIsOrderSensitive(t *testing.T) {
	// A commit is not its own ancestor for this purpose: the doc was verified AT that commit, so
	// that commit's change is exactly what was verified.
	tip := LastTouched(repoRoot(t), "crank.go")
	if tip == "" {
		t.Skip("no git history")
	}
	if IsAncestor(repoRoot(t), tip, tip) {
		t.Error("a commit must not count as an ancestor of itself — that would make every fresh stamp instantly stale")
	}
}
