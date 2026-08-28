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
	"strings"

	"partyline.sh/partyline/internal/features"
	"partyline.sh/partyline/internal/surfacescan"
)

// File is one generated artifact: a repo-relative path and its full contents.
type File struct {
	Path string
	Body []byte
	// Mode is the file's permission bits; zero means 0644.
	//
	// THIS EXISTS BECAUSE A MISSING +x SILENTLY BROKE EVERY SELF-HOST INSTALL. The repo's
	// deploy/stack/init/00-bootstrap.sh is 100755, but the published copy went out 100644, so
	// Postgres logged "bad interpreter: Permission denied", SKIPPED it, and then reported healthy
	// — a database with no auth schema and no roles, behind a green health check. All 114
	// migrations then fail at 0001_core.sql line 11, exactly as that script's own header predicts.
	Mode os.FileMode
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
	// The concepts section of llms-full.txt is extracted from the docs pages, so a docs page that
	// has dropped a concept — or carries one nobody has verified — stops generation here rather
	// than shipping an artifact that is quietly missing the half a model most needs.
	concepts, err := loadConcepts(root)
	if err != nil {
		return nil, err
	}
	// The llms files carry hand-written prose alongside their generated lists, so generation can
	// fail for a second reason: a sentence naming a page that no longer exists stops the build
	// rather than shipping a confident lie to the one audience that cannot check.
	short, err := genLLMsShort(s)
	if err != nil {
		return nil, err
	}
	full, err := genLLMsFull(root, s, reg, concepts)
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
		{Path: "web/src/lib/generated/self-host-env.ts", Body: genSelfHostEnvTS(reg)},
	}
	// The licence the SERVER is under, published because a self-hoster has to be able to read what
	// governs the thing they are installing BEFORE they install it — and the monorepo it lives in is
	// private, so a link to it is a 404 for exactly the audience that needs it.
	//
	// This is the ROOT licence (ELv2), deliberately not .cli-public/LICENSE (MIT). The CLI is a
	// client and is permissive; the server is not. Publishing the wrong one here would tell a
	// self-hoster they may resell the thing they may not.
	licence, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		return nil, fmt.Errorf("publishing LICENSE: %w", err)
	}
	// The banner every generated artifact carries. Worded carefully because this one is a legal
	// text: it describes the FILE (a copy, do not edit it, here is how it is regenerated) and says
	// plainly that the licence below is unmodified, so nothing here can read as amending the terms.
	licenceBanner := "GENERATED — DO NOT EDIT. This is a published copy of partyline's LICENSE, served here so\n" +
		"it can be read before anything is installed. To change it, edit the LICENSE in the partyline\n" +
		"repository and run `make surface-gen`; editing this copy changes nothing.\n\n" +
		"The licence text below is reproduced unmodified and is what governs.\n\n" +
		"----------------------------------------------------------------------------\n\n"
	files = append(files, File{Path: "web/public/self-host/LICENSE", Body: []byte(licenceBanner + string(licence))})

	// The schema, as a deterministic archive. See migrations.go for why it is an archive and not
	// 170 loose files.
	tarball, err := migrationsTarball(root)
	if err != nil {
		return nil, err
	}
	files = append(files, File{Path: migrationsArchive, Body: tarball})

	// The self-host stack, served from the marketing site so it is fetchable before anything is
	// installed (epic H.8 / constraint #631). See selfhost.go for why these are copies.
	for _, p := range []struct {
		dst, src, note string
		subs           []string
	}{
		{"web/public/self-host/docker-compose.yml", "deploy/stack/docker-compose.yml",
			"One line differs from ours, and it is the registry: the published copy pulls from Docker Hub, " +
				"which is anonymously pullable. Our own boxes pull the same digests from GHCR, which they " +
				"authenticate to. Set WEB_IMAGE / RELAY_IMAGE in your .env to point somewhere else. " +
				"RELAY_PORT and WEB_TAG come from your .env too; the relay service is optional for a single-box install.",
			[]string{
				"ghcr.io/partyline-sh/partyline-web:${WEB_TAG:-latest}", "partyline/partyline-web:${WEB_TAG:-latest}",
				"ghcr.io/partyline-sh/partyline-relay:${RELAY_TAG:-latest}", "partyline/partyline-relay:${RELAY_TAG:-latest}",
				// PIN THE PLATFORM, because the published images are linux/amd64 ONLY. On any ARM
				// host — Apple Silicon, Graviton, Ampere, Hetzner's own ARM line — an unpinned pull
				// asks for linux/arm64/v8, finds no matching manifest, and stops with an error that
				// tells the reader nothing. Pinned, the same image runs under emulation. Overridable
				// so a multi-arch build later needs no edit here.
				"    image: ${WEB_IMAGE:-partyline/partyline-web:${WEB_TAG:-latest}}",
				"    image: ${WEB_IMAGE:-partyline/partyline-web:${WEB_TAG:-latest}}\n    platform: ${SELFHOST_PLATFORM:-linux/amd64}",
				"    image: ${RELAY_IMAGE:-partyline/partyline-relay:${RELAY_TAG:-latest}}",
				"    image: ${RELAY_IMAGE:-partyline/partyline-relay:${RELAY_TAG:-latest}}\n    platform: ${SELFHOST_PLATFORM:-linux/amd64}",
			}},
		{"web/public/self-host/00-bootstrap.sh", "deploy/stack/init/00-bootstrap.sh",
			"Verbatim. Mount it at /docker-entrypoint-initdb.d/00-bootstrap.sh — it runs ONCE, on an empty data dir, before any migration.", nil},
		// The docs told strangers "apply-migrations.sh reads that directory" and never published it:
		// /self-host/apply-migrations.sh was a 404, so the documented install stopped dead after
		// extracting the schema with no sanctioned way to apply it.
		{"web/public/self-host/apply-migrations.sh", "deploy/stack/apply-migrations.sh",
			"Verbatim. Run it from the directory holding ./migrations (see the self-host guide); it is idempotent and records every file in a schema_migrations ledger.", nil},
	} {
		body, err := publishedStackFile(root, p.src, p.note, p.subs...)
		if err != nil {
			return nil, err
		}
		// A published shell script must arrive executable — see File.Mode.
		mode := os.FileMode(0)
		if strings.HasSuffix(p.dst, ".sh") {
			mode = 0o755
		}
		files = append(files, File{Path: p.dst, Body: body, Mode: mode})
	}
	caddy, err := publishedStackFile(root, "deploy/stack/Caddyfile.staging",
		"Our staging edge, with the hostname replaced by partyline.example.com and nothing else changed. Put your own hostname in, and drop the X-Robots-Tag block (that one exists only to keep staging out of search results).",
		"staging.partyline.sh", "partyline.example.com")
	if err != nil {
		return nil, err
	}
	files = append(files, File{Path: "web/public/self-host/Caddyfile", Body: caddy})
	// env-bootstrap generates the secrets and MINTS the anon / service-role JWTs, which are signed
	//
	// (Hyphenated on purpose. The CLI mirror refuses to publish a tree containing the underscored
	// form — it is one of the SECRET_NEVER shapes, unconditional and with no allowlist, because that
	// literal is what a leaked key looks like. This comment tripped it and failed v0.78.0's source
	// mirror after the binaries had already published. The scanner is right to have no exception for
	// prose: an exception is how the next one gets through.)
	// with SESSION_JWT_SECRET and therefore cannot be copied from anywhere — without it a
	// self-hoster has no way to produce a working PGRST_ANON_KEY at all.
	//
	// ONE block is replaced: the prod-owns-port-22 rule, which exists only because pppp.sh:22 is
	// compiled into shipped CLIs and on anyone else's box would collide with sshd.
	//
	// A second substitution used to strip our own R2 account id and bucket names. H.4 removed that
	// block from the script entirely — MinIO is the default now and R2_* is only carried over when
	// a box already had it — so there is nothing left to redact, and publishedStackFile correctly
	// refused to generate rather than let a stale redaction pass silently. Verified before removing
	// it: the account id, both bucket names and the R2 endpoint appear zero times in the script.
	bootstrap, err := publishedStackFile(root, "scripts/env-bootstrap.sh",
		"One block differs from ours, replaced deliberately and marked in the script: the rule that gives production port 22. That exists because pppp.sh:22 is compiled into every shipped CLI; on your box it would collide with sshd, so the published copy defaults to 2222.",
		`case "$SITE" in
  *staging*) put RELAY_PORT "2222" ;;
  *)         put RELAY_PORT "22" ;;
esac`,
		`# CHANGED FOR SELF-HOST: production takes :22 only because "pppp.sh:22" is compiled into every
# shipped CLI. On your box :22 is sshd, so the relay stays on 2222 and joiners dial it explicitly.
put RELAY_PORT "2222"`,
		`echo "STILL MISSING — real credentials, set them with scripts/staging-secrets.sh:"`,
		`echo "STILL MISSING — real credentials. Append them to .env by hand; it is chmod 600:"`)

	if err != nil {
		return nil, err
	}
	files = append(files, File{Path: "web/public/self-host/env-bootstrap.sh", Body: bootstrap, Mode: 0o755})
	// ALSO into deploy/stack, because `ptln server bootstrap` may only name artifacts it has
	// CHECKED exist on the machine (TestThePlanNeverFetchesFromAURL) — a plan that says "curl this
	// and run it" is telling someone to execute code nobody verified. Generated from the same
	// source as the published copy, so the two cannot drift.
	files = append(files, File{Path: "deploy/stack/env-bootstrap.sh", Body: bootstrap, Mode: 0o755})
	// The annotated variable list. NOT byte-identical to ours: the published copy carries no values
	// and says not to copy it to .env — see genEnvReferencePublic for the silent failure that
	// otherwise follows from the idempotent bootstrap.
	files = append(files, File{Path: "web/public/self-host/env.example", Body: genEnvReferencePublic(reg)})
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
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		if old, err := os.ReadFile(abs); err == nil && bytes.Equal(old, f.Body) {
			// SAME BYTES IS NOT SAME FILE. An early return here is what let 00-bootstrap.sh sit at
			// 0644 forever: its contents never changed, so the writer skipped it every run and the
			// missing +x was never corrected. Reconcile the mode before deciding nothing is stale.
			if st, err := os.Stat(abs); err == nil && st.Mode().Perm() != mode.Perm() {
				if err := os.Chmod(abs, mode); err != nil {
					return changed, err
				}
				changed = append(changed, f.Path)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return changed, err
		}
		if err := os.WriteFile(abs, f.Body, mode); err != nil {
			return changed, err
		}
		// WriteFile only applies the mode when it CREATES the file; an existing one keeps its own.
		if err := os.Chmod(abs, mode); err != nil {
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
