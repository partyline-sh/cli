package surfacegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/clispec"
	"partyline.sh/partyline/internal/features"
)

const repoRoot = "../.."

// A generated artifact feeds a diff-based drift gate. If generation were not deterministic the
// gate would fire on every run and be turned off within a week.
func TestGenerationIsDeterministic(t *testing.T) {
	a, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("file count differs between runs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Path != b[i].Path {
			t.Fatalf("file order differs: %s vs %s", a[i].Path, b[i].Path)
		}
		if string(a[i].Body) != string(b[i].Body) {
			t.Errorf("%s differs between two runs of the generator", a[i].Path)
		}
	}
}

// Every artifact must announce that it is generated. A reader who has never heard of this package
// still needs to know not to edit the file, and to know the one command that regenerates it.
func TestEveryArtifactCarriesTheBanner(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		body := string(f.Body)
		// llms.txt is consumed by language models as prose; a do-not-edit banner would be noise
		// in the one place the audience is a model. It is covered by the drift check instead.
		if strings.HasPrefix(f.Path, "web/public/llms") {
			continue
		}
		// The migrations archive is a gzip stream: there is nowhere to put a text banner, and the
		// point of shipping it as an archive is that the SQL inside is byte-identical to what ran
		// against production. Annotating those files would defeat that. Covered by the determinism
		// check above and by the drift check, exactly like llms.txt.
		if strings.HasSuffix(f.Path, ".tar.gz") {
			continue
		}
		if !strings.Contains(body, "GENERATED — DO NOT EDIT") {
			t.Errorf("%s has no do-not-edit banner", f.Path)
		}
		if !strings.Contains(body, "make surface-gen") {
			t.Errorf("%s does not say how to regenerate it", f.Path)
		}
	}
}

// The drift gate itself. If Check cannot notice a hand edit, `make surface-check` is decoration.
func TestCheckDetectsAHandEdit(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no artifacts declared")
	}
	target := filepath.Join(repoRoot, filepath.FromSlash(files[0].Path))
	original, err := os.ReadFile(target)
	if err != nil {
		t.Skipf("%s not generated yet — run `make surface-gen`", files[0].Path)
	}
	t.Cleanup(func() { _ = os.WriteFile(target, original, 0o644) })

	if stale, _ := Check(repoRoot); len(stale) != 0 {
		t.Fatalf("tree is already stale before the mutation: %v — run `make surface-gen`", stale)
	}
	if err := os.WriteFile(target, append(original, []byte("\n// a human edited this\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err := Check(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0] != files[0].Path {
		t.Errorf("Check() = %v, want exactly [%s] — a hand edit went unnoticed", stale, files[0].Path)
	}
}

// The llms files exist to tell a model what partyline IS. The versions on disk before this package
// described a three-feature product and had not been touched since 5 July. Assert the generated
// ones actually mention the capabilities that make up most of the product now, so a future edit to
// the prose constants cannot quietly regress them.
func TestLLMsFilesDescribeTheCurrentProduct(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, "web/public/llms") {
			continue
		}
		body := strings.ToLower(string(f.Body))
		for _, want := range []string{"crank", "worktree", "verify gate", "context threads", "daemon", "fleet", "work board", "/work"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s never mentions %q", f.Path, want)
			}
		}
		// The support matrix is the specific place overclaiming is tempting and harmful.
		if !strings.Contains(body, "claude code only") {
			t.Errorf("%s does not state the Context Threads engine limitation", f.Path)
		}
	}
}

// The published env reference must carry NO values, and must say it is not a template.
//
// The failure this guards is silent and unrecoverable-by-retry: env-bootstrap.sh only ever ADDS a
// variable that is missing, so a self-hoster who seeds .env from a file containing our STAGING
// examples (SITE_URL=https://staging.partyline.sh, PGRST_URL=http://postgrest:3000, WEB_TAG=staging)
// gets a box pointed at our hostnames, with no error anywhere, that re-running the bootstrap will
// never correct. Our own deploy/stack/env.example keeps its examples — that file is for a box we
// operate — which is exactly why the two copies have to be asserted apart.
func TestPublishedEnvReferenceCarriesNoValues(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, f := range files {
		if f.Path == "web/public/self-host/env.example" {
			body = string(f.Body)
		}
	}
	if body == "" {
		t.Fatal("web/public/self-host/env.example is not generated any more")
	}
	if !strings.Contains(body, "DO NOT COPY THIS FILE TO .env") {
		t.Error("the published reference does not warn against copying it to .env")
	}
	for i, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, val, ok := strings.Cut(line, "="); ok && val != "" {
			t.Errorf("line %d assigns a value a reader could paste: %q", i+1, line)
		}
	}
	// Our own copy is the control: if it ever stops carrying examples, this test is passing for the
	// wrong reason and the split has collapsed.
	for _, f := range files {
		if f.Path == "deploy/stack/env.example" && !strings.Contains(string(f.Body), "=staging") {
			t.Error("deploy/stack/env.example no longer carries example values — the two copies have converged")
		}
	}
}

// "Required" must mean "you set this", not "the app reads this".
//
// The docs table originally listed the whole Core class as required, which told a self-hoster to go
// and set NODE_ENV, NEXT_RUNTIME and two NEXT_PUBLIC_SUPABASE_* variables. Next sets the first two
// itself, and NEXT_PUBLIC_* is inlined into the image at BUILD time — so a value in .env is read by
// nothing, and the two that look most like configuration would send someone to point PostgREST at
// the one place it is not read from.
//
// The check is grounded in behaviour rather than in an opinion about each variable: every name in
// the required group must be one scripts/env-bootstrap.sh actually writes to .env, and nothing in
// the external group may be. A new Core variable therefore cannot land silently — either the
// bootstrap writes it (required) or someone states why it is external.
func TestRequiredEnvIsWhatTheBootstrapWrites(t *testing.T) {
	reg, err := features.Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts/env-bootstrap.sh"))
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]bool{}
	for _, line := range strings.Split(string(script), "\n") {
		if f := strings.Fields(strings.TrimSpace(line)); len(f) >= 2 && f[0] == "put" {
			written[f[1]] = true
		}
	}
	if len(written) < 5 {
		t.Fatalf("parsed only %d `put` lines out of env-bootstrap.sh — the parser, not the data, is wrong", len(written))
	}

	for _, r := range selfHostEnv(reg) {
		switch r.Group {
		case "required":
			if !written[r.Name] {
				t.Errorf("%s is shown as REQUIRED but env-bootstrap.sh never writes it — either the "+
					"bootstrap should set it, or it belongs in notInDotEnv with the reason stated", r.Name)
			}
		case "external":
			if written[r.Name] {
				t.Errorf("%s is listed in notInDotEnv as not-yours-to-set, but env-bootstrap.sh does "+
					"write it — one of the two is now wrong", r.Name)
			}
		}
	}
}

// The self-host page may not name a `ptln server` subcommand the CLI does not have.
//
// This package owns the self-host doc surface — the page's configuration table and every stack file
// it tells a reader to fetch are generated here — so the page's claims about the CLI belong under
// the same gate. The specific failure it prevents already happened twice in one direction each: the
// page correctly declared `ptln server bootstrap` unbuilt while a comment in THIS package described
// H.7's exclusion table as if it existed, and a reviewer reasonably read the two together as the
// docs lying about the CLI. A doc naming a command that does not exist is the phantom case the whole
// docs audit was built for; this is that check for prose the audit's anchor syntax cannot reach.
//
// An unbuilt command may still be NAMED — saying "this does not exist yet" is the honest thing to do
// and the task asked for it — so the escape hatch is stating so on the same line.
func TestSelfHostPageNamesOnlyRealServerSubcommands(t *testing.T) {
	page := filepath.Join(repoRoot, "web/src/app/(marketing)/docs/self-host/page.mdx")
	body, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := clispec.Lookup("server")
	if !ok {
		t.Fatal(`clispec has no "server" command — the page documents one`)
	}
	real := map[string]bool{"doctor": true} // seeded so a truncated Subs list cannot pass vacuously
	for _, s := range spec.Subs {
		name, _, _ := strings.Cut(s, ":")
		real[strings.TrimSpace(name)] = true
	}

	named := regexp.MustCompile(`ptln server ([a-z][a-z-]*)`)
	for i, line := range strings.Split(string(body), "\n") {
		for _, m := range named.FindAllStringSubmatch(line, -1) {
			sub := m[1]
			if real[sub] || strings.Contains(line, "Not built") {
				continue
			}
			t.Errorf("line %d names `ptln server %s`, which clispec does not declare. Either build it, "+
				"or say \"Not built\" on the same line — never describe it as if it works.", i+1, sub)
		}
	}
}
