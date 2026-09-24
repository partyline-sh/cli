package surfacescan

import (
	"os"
	"path/filepath"
	"strings"
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
func TestSchemaReplayAppliesMigrationsInOrder(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"supabase/migrations/0001_init.sql": `
create table public.widgets (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  price numeric(10,2),
  doomed text,
  constraint widgets_name_key unique (name),
  check (price > 0)
);
create table public.temporary_thing ( id uuid primary key );
`,
		"supabase/migrations/0002_later.sql": `
alter table public.widgets add column if not exists colour text;
alter table public.widgets drop column if exists doomed;
drop table if exists public.temporary_thing;
`,
		// ONE statement, SEVERAL clauses — the shape every recent migration uses. Matching
		// "alter table X add column Y" once per statement finds only the first, which is exactly
		// how the gate migration's three columns landed in the reference as one.
		"supabase/migrations/0003_multi.sql": `
alter table public.widgets
  add column if not exists weight numeric,
  add column if not exists height numeric,
  drop column if exists price;
alter table public.widgets add constraint widgets_weight_check check (weight > 0);
`,
		// A commented-out statement must not enter the schema.
		"supabase/migrations/0004_comment.sql": `
-- create table public.never_real ( id uuid );
`,
	})

	items := scanTables(root)
	w, ok := itemByRef(items, "table:widgets")
	if !ok {
		t.Fatal("widgets table not extracted")
	}
	got := strings.Join(w.Detail, ",")
	if want := "colour,height,id,name,weight"; got != want {
		t.Errorf("widgets columns = %q, want %q\n  (colour added; doomed and price dropped; every clause of a\n   multi-clause ALTER applied; no constraint clauses treated as columns)", got, want)
	}
	if _, ok := itemByRef(items, "table:temporary_thing"); ok {
		t.Error("a dropped table is still reported")
	}
	if _, ok := itemByRef(items, "table:never_real"); ok {
		t.Error("a table inside a -- comment entered the schema")
	}
	// The cited source is the migration that INTRODUCED the table, not the last to touch it.
	if !strings.HasSuffix(w.Source, "0001_init.sql") {
		t.Errorf("widgets source = %q, want the introducing migration", w.Source)
	}
}

// A table rename carries its columns to the new name — the reference must document the name that
// exists, not the one it was born with. (The `deployments` → `trigger_events` rename is the first.)
func TestSchemaReplayFollowsATableRename(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"supabase/migrations/0001_init.sql": `
create table public.deployments (
  id uuid primary key,
  environment text,
  outcome text not null
);
`,
		"supabase/migrations/0002_rename.sql": `
alter table if exists public.deployments rename to trigger_events;
alter table public.trigger_events rename constraint deployments_pkey to trigger_events_pkey;
alter table public.trigger_events add column if not exists kind text;
`,
	})

	items := scanTables(root)
	if _, ok := itemByRef(items, "table:deployments"); ok {
		t.Error("the pre-rename table name is still reported")
	}
	e, ok := itemByRef(items, "table:trigger_events")
	if !ok {
		t.Fatal("renamed table not extracted")
	}
	got := strings.Join(e.Detail, ",")
	if want := "environment,id,kind,outcome"; got != want {
		t.Errorf("trigger_events columns = %q, want %q\n  (columns carried across the rename; a later ALTER still applies;\n   `rename constraint` did not rename the table again)", got, want)
	}
	// Still cite where the table came from — the rename migration has no columns in it.
	if !strings.HasSuffix(e.Source, "0001_init.sql") {
		t.Errorf("trigger_events source = %q, want the introducing migration", e.Source)
	}
}

// ---- api routes ----

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

	api := scanAPIRoutes(root)
	things, ok := itemByRef(api, "api:/api/v1/things")
	if !ok {
		t.Fatal("collection route not extracted")
	}
	if got := strings.Join(things.Detail, ","); got != "GET,POST" {
		t.Errorf("methods = %q, want GET,POST", got)
	}
	if item, ok := itemByRef(api, "api:/api/v1/things/[id]"); !ok || strings.Join(item.Detail, ",") != "DELETE" {
		t.Errorf("dynamic route missing or wrong methods: %+v", item)
	}
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
	for _, kind := range []string{"api", "web", "table", "env", "vocab"} {
		if n := len(s.OfKind(kind)); n == 0 {
			t.Errorf("facet %q extracted nothing — its matcher is broken", kind)
		}
	}
	// Anchors that exist today and whose disappearance means the scanner broke, not the repo.
	for _, ref := range []string{"table:run_tasks", "table:runs", "vocab:gate_code", "api:/api/v1/me"} {
		if !s.Has(ref) {
			t.Errorf("expected %s in the extracted surface", ref)
		}
	}
	if item, ok := itemByRef(s.Items, "table:run_tasks"); ok {
		for _, col := range []string{"status", "branch", "resume_handle", "pr_url"} {
			if !contains(item.Detail, col) {
				t.Errorf("run_tasks is missing column %q — the migration replay dropped it", col)
			}
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
