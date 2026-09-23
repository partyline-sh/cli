package surfacegen

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The database schema, published so a self-hoster can actually get it.
//
// WHY. The stack files have been fetchable for a while — docker-compose.yml, env.example,
// env-bootstrap.sh — but the schema was not. It lives in the monorepo, which is private, and the
// deploy rsyncs it straight onto the box. So the self-host page could describe every step and a
// stranger still could not complete one: apply-migrations.sh needs the files, and the only way to
// get them was repo access.
//
// AS AN ARCHIVE, not 170 loose files. apply-migrations.sh expects a `migrations/` directory, one
// download reconstructs it exactly, and — the part that matters — the bytes inside are IDENTICAL to
// the repository's. A per-file copy would have to carry the generator's do-not-edit banner, which
// means editing SQL that has already been applied to production databases. A published migration
// that differs from the one that ran is a trap, even when the difference is a comment.
const migrationsArchive = "web/public/self-host/migrations.tar.gz"

// migrationsTarball builds a deterministic gzip of supabase/migrations.
//
// DETERMINISM IS A REQUIREMENT, not a nicety: TestGeneratorIsDeterministic compares two runs byte
// for byte, and both tar and gzip embed timestamps by default — a naive implementation differs on
// every invocation and turns `make surface-check` into a permanent false positive. So entries are
// sorted, every mtime is zeroed, ownership is dropped, and the gzip header carries no name or time.
func migrationsTarball(root string) ([]byte, error) {
	dir := filepath.Join(root, "supabase", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("publishing migrations: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// BASELINE has no extension and is load-bearing — apply-migrations.sh reads it to decide
		// whether a database with existing schema should record history rather than replay it.
		if strings.HasSuffix(e.Name(), ".sql") || e.Name() == "BASELINE" {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("publishing migrations: %s has no .sql files", dir)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	// Level 9: written once per generation, fetched by strangers on a first install.
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Header.ModTime = time.Time{} // no timestamp, or two runs differ
	zw.Header.Name = ""
	tw := tar.NewWriter(zw)

	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("publishing migrations: %w", err)
		}
		// Prefixed so `tar xzf` produces the `migrations/` directory apply-migrations.sh expects,
		// rather than scattering 170 files into whatever directory the reader happened to be in.
		if err := tw.WriteHeader(&tar.Header{
			Name:     "migrations/" + name,
			Mode:     0o644,
			Size:     int64(len(body)),
			ModTime:  time.Time{},
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR, // PAX records would embed timestamps again
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
