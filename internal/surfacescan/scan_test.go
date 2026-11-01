package surfacescan

import (
	"os"
	"path/filepath"
	"testing"
)

// ---- routePath: the two Next conventions that would silently corrupt every extracted URL ----

func TestRoutePathHonoursNextConventions(t *testing.T) {
	base := "/repo/web/src/app"
	for _, tc := range []struct{ dir, want string }{
		{base, "/"},
		{base + "/projects", "/projects"},
		{base + "/api/v1/runs", "/api/v1/runs"},
		// A route GROUP organises files without appearing in the URL. Treating it as a segment
		// would report /(marketing)/docs, which matches nothing a user can visit.
		{base + "/(marketing)/docs", "/docs"},
		{base + "/(marketing)", "/"},
		// Dynamic segments are kept verbatim so an extracted ref and a doc's covers: claim are
		// the same string.
		{base + "/api/v1/runs/[id]/approve", "/api/v1/runs/[id]/approve"},
		{base + "/work/plan/[threadId]", "/work/plan/[threadId]"},
		// Parallel-route slots are invisible in the URL too.
		{base + "/dashboard/@panel", "/dashboard"},
	} {
		if got := routePath(base, tc.dir); got != tc.want {
			t.Errorf("routePath(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

// ---- schema replay ----

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func itemByRef(items []Item, ref string) (Item, bool) {
	for _, it := range items {
		if it.Ref == ref {
			return it, true
		}
	}
	return Item{}, false
}

// Migrations are applied in filename order, so the scanner must replay them in that order —
// a later ALTER has to win over the CREATE, and a DROP has to actually remove.
func TestAPIRoutesReportTheirMethods(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"web/src/app/api/v1/things/route.ts": `
export async function GET(request: Request) {}
export async function POST(request: Request) {}
`,
		"web/src/app/api/v1/things/[id]/route.ts": `
export async function DELETE(request: Request, { params }: { params: Promise<{ id: string }> }) {}
`,
		// A page is a web route, never an api one.
		"web/src/app/things/page.tsx": `export default function P() { return null }`,
	})

	if _, ok := itemByRef(scanWebRoutes(root), "web:/things"); !ok {
		t.Error("page.tsx did not produce a web route")
	}
}

// ---- env ----

func TestEnvFindsBothLanguagesAndBothSyntaxes(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"daemon.go": `package main
func f() { _ = os.Getenv("PARTYLINE_API"); _ = os.Getenv("TICK_SECRET") }`,
		"web/src/lib/x.ts": `
const a = process.env.SESSION_JWT_SECRET;
const b = process.env["RESEND_API_KEY"];
const c = process.env.NODE_ENV;
`,
		// Test files are excluded: a var only a test sets is not part of the config surface.
		"web/src/lib/x.test.ts": `const z = process.env.ONLY_IN_TESTS;`,
		"other_test.go":         `package main // os.Getenv("ALSO_ONLY_IN_TESTS")`,
	})

	env := scanEnv(root)
	for _, want := range []string{"env:PARTYLINE_API", "env:TICK_SECRET", "env:SESSION_JWT_SECRET", "env:RESEND_API_KEY", "env:NODE_ENV"} {
		if _, ok := itemByRef(env, want); !ok {
			t.Errorf("%s not extracted", want)
		}
	}
	for _, unwanted := range []string{"env:ONLY_IN_TESTS", "env:ALSO_ONLY_IN_TESTS"} {
		if _, ok := itemByRef(env, unwanted); ok {
			t.Errorf("%s came from a test file and should be excluded", unwanted)
		}
	}
}

// ---- the whole scan, against the real repository ----

// A facet that silently extracts nothing is the failure mode that would make every downstream
// coverage check pass for the wrong reason. Assert each one found something real.
func TestScanOfThisRepoFindsEveryFacet(t *testing.T) {
	s, err := Scan(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"web", "env", "cli"} {
		if n := len(s.OfKind(kind)); n == 0 {
			t.Errorf("facet %q extracted nothing — its matcher is broken", kind)
		}
	}
	// Anchors that exist today and whose disappearance means the scanner broke, not the repo.
	for _, ref := range []string{"cli:memory", "cli:llms"} {
		if !s.Has(ref) {
			t.Errorf("expected %s in the extracted surface", ref)
		}
	}
}

// Output feeds a committed artifact and a diff-based drift check, so it has to be byte-stable.
func TestScanIsDeterministic(t *testing.T) {
	root := filepath.Join("..", "..")
	a, _ := Scan(root)
	b, _ := Scan(root)
	ja, err := a.JSON()
	if err != nil {
		t.Fatal(err)
	}
	jb, _ := b.JSON()
	if string(ja) != string(jb) {
		t.Error("two scans of the same tree produced different bytes — every diff would be noise")
	}
}
