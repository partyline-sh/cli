package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// stack_embed.go — the compose stack, carried INSIDE the binary.
//
// WHY IT IS EMBEDDED RATHER THAN FETCHED. `ptln server install` has to work on a box that has
// nothing but this binary: no repo checkout, no network to a raw.githubusercontent URL (deploy/ is
// pruned from the public CLI mirror, so no such URL exists), no prior partyline install. Anything
// fetched at install time is a step that can fail on a firewalled or air-gapped box; files carried
// in the binary cannot. It also version-locks the stack to the CLI that installs it, which is the
// property that makes a re-run reconcile rather than mix two generations of compose file.
//
// It is 77 KB of text. That is a rounding error against the binary and buys an installer with no
// network dependency at all.
//
// NOTE FOR THE MIRROR: this embed means deploy/stack must survive into the public CLI tree or the
// published source stops compiling — the same trap docs/partyline.1 fell into (see
// scripts/mirror-cli.sh, which now restores it deliberately).

//go:embed all:deploy/stack
var stackFS embed.FS

const stackRoot = "deploy/stack"

// The schema, for the same reason. apply-migrations.sh reads <install>/migrations and FAILS rather
// than silently succeeding when it finds nothing there — "a migration step that passes by finding
// zero files is how an empty database reaches production looking green". So the installer has to
// put them there, which means carrying them.
//
//go:embed all:supabase/migrations
var migrationsFS embed.FS

const migrationsRoot = "supabase/migrations"

// stackAssets lists every embedded stack path, relative to the stack root.
func stackAssets() ([]string, error) {
	var out []string
	err := fs.WalkDir(stackFS, stackRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stackRoot, p)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}

// readStackAsset returns one embedded stack file by its path relative to the stack root.
func readStackAsset(rel string) ([]byte, error) {
	return stackFS.ReadFile(stackRoot + "/" + rel)
}

// isExecStackAsset reports whether a materialized stack file needs the executable bit. embed drops
// file modes, so they have to be reapplied by name — a shipped .sh that lands 0644 turns the first
// `./apply-migrations.sh` into "permission denied", which reads as a broken download.
func isExecStackAsset(rel string) bool {
	return strings.HasSuffix(rel, ".sh")
}

// writeStack materializes the embedded stack into dir.
//
// It never overwrites a file whose content already matches, and it never touches .env — that file
// holds the box's secrets, and an installer that rewrites it turns a re-run into an outage. Returns
// the paths it actually wrote, so the caller can report exactly what changed and nothing more.
func writeStack(dir string) ([]string, error) {
	assets, err := stackAssets()
	if err != nil {
		return nil, fmt.Errorf("read embedded stack: %w", err)
	}
	if len(assets) == 0 {
		// Fail loudly: an empty embed means a build problem, and silently installing nothing
		// would leave the operator debugging a box that never had a stack on it.
		return nil, fmt.Errorf("the embedded stack is empty — this is a bug in this build, not a problem with this machine")
	}

	var wrote []string
	for _, rel := range assets {
		body, err := readStackAsset(rel)
		if err != nil {
			return wrote, fmt.Errorf("read embedded %s: %w", rel, err)
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return wrote, fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
		}
		mode := os.FileMode(0o644)
		if isExecStackAsset(rel) {
			mode = 0o755
		}
		if same, err := fileHasContent(dst, body, mode); err == nil && same {
			continue
		}
		if err := os.WriteFile(dst, body, mode); err != nil {
			return wrote, fmt.Errorf("write %s: %w", dst, err)
		}
		if err := os.Chmod(dst, mode); err != nil {
			return wrote, fmt.Errorf("chmod %s: %w", dst, err)
		}
		wrote = append(wrote, rel)
	}
	return wrote, nil
}

// fileHasContent reports whether path already holds exactly body with the right mode bits.
func fileHasContent(path string, body []byte, mode os.FileMode) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if st.Mode().Perm() != mode.Perm() {
		return false, nil
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return string(cur) == string(body), nil
}

// writeMigrations materializes the embedded schema into <dir>/migrations, where
// apply-migrations.sh looks for it.
func writeMigrations(dir string) (int, error) {
	dst := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, fmt.Errorf("create %s: %w", dst, err)
	}
	entries, err := fs.ReadDir(migrationsFS, migrationsRoot)
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := migrationsFS.ReadFile(migrationsRoot + "/" + e.Name())
		if err != nil {
			return n, fmt.Errorf("read embedded migration %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), body, 0o644); err != nil {
			return n, fmt.Errorf("write %s: %w", e.Name(), err)
		}
		n++
	}
	if n == 0 {
		// Same reasoning as apply-migrations.sh: zero files must never look like success.
		return 0, fmt.Errorf("the embedded schema is empty — this is a bug in this build")
	}
	return n, nil
}
