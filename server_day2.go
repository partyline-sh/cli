package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// server_day2.go — the three commands a self-hosted box needs AFTER the install.
//
// Install pain got the attention because it comes first, but it happens once. What actually costs
// operators data and weekends is day two: upgrading in the wrong order, having no backup when a
// disk dies, and reading `docker compose ps` output while something is down. One command each.
//
//	ptln server upgrade   refresh the stack + schema from THIS binary, pull, restart, migrate
//	ptln server backup    the database and the config, one file, restore instructions printed
//	ptln server status    what is running, what is not, and the command that shows why
//
// All three locate the install the same way and refuse plainly when there isn't one.

// findInstallDir locates an existing install: PARTYLINE_DIR, then the two default locations. An
// install is a directory holding both docker-compose.yml and .env — the compose file alone is just
// stack files, and .env alone is just secrets.
func findInstallDir(stat func(string) (os.FileInfo, error)) (string, error) {
	var candidates []string
	if d := strings.TrimSpace(os.Getenv("PARTYLINE_DIR")); d != "" {
		candidates = append(candidates, strings.TrimRight(d, "/"))
	}
	home, _ := os.UserHomeDir()
	candidates = append(candidates, defaultInstallDir)
	if home != "" {
		candidates = append(candidates, filepath.Join(home, "partyline"))
	}
	for _, d := range candidates {
		if _, err := stat(filepath.Join(d, "docker-compose.yml")); err != nil {
			continue
		}
		if _, err := stat(filepath.Join(d, ".env")); err != nil {
			continue
		}
		return d, nil
	}
	return "", fmt.Errorf("no partyline install found (looked in %s) — `ptln server install` sets one up, and PARTYLINE_DIR points here at one somewhere else",
		strings.Join(candidates, ", "))
}

// readEnvValue reads one value from the box's .env. Local use only: values from this file are
// secrets and must never reach output — callers take ports and hostnames, nothing else.
func readEnvValue(path, name string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(body), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ── upgrade ─────────────────────────────────────────────────────────────────────────────────────

// serverUpgrade refreshes an existing install from THIS binary and restarts it, in the order that
// cannot serve new code against an old schema: files, images, containers, migrations, health.
//
// The stack and every migration are embedded, so `ptln update && ptln server upgrade` is the whole
// upgrade path — no checkout, no download beyond the images. It reuses the installer's writers,
// which never rewrite an edited Caddyfile and never touch .env, so an upgrade cannot lose an
// operator's configuration; and every step asserts its effect the way the installer does.
func serverUpgrade(dir string, ops installOps) bool {
	fmt.Fprintf(ops.out, "upgrading the install at %s\n\n", dir)

	steps := []struct {
		what string
		do   func() error
	}{
		{"refresh the stack files", func() error { _, err := writeStack(dir); return err }},
		{"refresh the database schema", func() error { _, err := writeMigrations(dir); return err }},
		{"pull the images", func() error {
			out, err := ops.run(dir, "docker", "compose", "pull", "--quiet")
			if err != nil {
				return fmt.Errorf("%s", installFirstLine(out))
			}
			return nil
		}},
		{"restart the stack", func() error {
			// --remove-orphans: services this version of the stack no longer defines (the
			// ticker) are retired rather than left running beside their replacement.
			out, err := ops.run(dir, "docker", "compose", "up", "-d", "--remove-orphans")
			if err != nil {
				return fmt.Errorf("%s", installFirstLine(out))
			}
			return nil
		}},
		{"apply database migrations", func() error {
			out, err := ops.run(dir, "./apply-migrations.sh")
			if err != nil {
				return fmt.Errorf("%s", installFirstLine(out))
			}
			return nil
		}},
	}
	for i, s := range steps {
		fmt.Fprintf(ops.out, "  [%d/%d] %s ... ", i+1, len(steps), s.what)
		if err := s.do(); err != nil {
			fmt.Fprintf(ops.out, "FAILED\n\n%v\n\nNothing needs undoing — fix the above and re-run; it reconciles.\n", err)
			return false
		}
		fmt.Fprintf(ops.out, "ok\n")
	}

	if msg := probeHealth(dir, ops); msg != "" {
		fmt.Fprintf(ops.out, "\n%s\n", msg)
		return false
	}
	fmt.Fprintf(ops.out, "\nUpgraded and serving.\n")
	return true
}

// probeHealth polls /api/health through the edge, using the ports the box's own .env names.
// Returns "" when healthy, otherwise what to look at.
func probeHealth(dir string, ops installOps) string {
	env := filepath.Join(dir, ".env")
	if readEnvValue(env, "CADDY_REPLICAS") == "0" {
		return "" // no edge of ours to probe through; container states are the signal
	}
	port := readEnvValue(env, "HTTPS_PORT")
	if port == "" {
		port = "443"
	}
	url := "https://127.0.0.1:" + port + "/api/health"
	code, err := pollHTTP(ops, url, 90*time.Second)
	if err != nil {
		return fmt.Sprintf("the stack restarted but %s did not respond: %v\nLogs: cd %s && docker compose logs --tail=50", url, err, dir)
	}
	if code != http.StatusOK {
		return fmt.Sprintf("%s answered %d, want 200\nLogs: cd %s && docker compose logs --tail=50", url, code, dir)
	}
	return ""
}

// ── backup ──────────────────────────────────────────────────────────────────────────────────────

// serverBackup writes one tar.gz holding the database dump and the box's configuration.
//
// The database streams straight to disk (runToFile) — a dump can be larger than memory, and a
// backup command that buffers the whole database is a command that fails exactly when the data
// got big enough to matter. The archive is 0600: it contains .env, which is every secret the box
// has.
func serverBackup(dir, outPath string, ops installOps) bool {
	if outPath == "" {
		outPath = fmt.Sprintf("partyline-backup-%s.tar.gz", time.Now().UTC().Format("20060102-150405"))
	}
	work, err := os.MkdirTemp("", "ptln-backup-")
	if err != nil {
		fmt.Fprintf(ops.out, "backup: %v\n", err)
		return false
	}
	defer os.RemoveAll(work)

	fmt.Fprintf(ops.out, "backing up the install at %s\n\n", dir)
	fmt.Fprintf(ops.out, "  [1/3] dumping the database ... ")
	dump := filepath.Join(work, "db.sql")
	if err := ops.runToFile(dir, dump, "docker", "compose", "exec", "-T", "postgres",
		"pg_dump", "-U", "postgres", "--clean", "--if-exists", "partyline"); err != nil {
		fmt.Fprintf(ops.out, "FAILED\n\n%v\nIs the stack running? docker compose up -d postgres\n", err)
		return false
	}
	fmt.Fprintf(ops.out, "ok\n  [2/3] collecting configuration ... ")

	files := []string{dump}
	for _, name := range []string{".env", "Caddyfile", "docker-compose.override.yml"} {
		src := filepath.Join(dir, name)
		if _, err := os.Stat(src); err != nil {
			continue // override and even Caddyfile are optional; .env absence fails below
		}
		files = append(files, src)
	}
	if len(files) < 2 || filepath.Base(files[1]) != ".env" {
		fmt.Fprintf(ops.out, "FAILED\n\nno .env at %s — a backup without the secrets cannot restore the database it contains\n", dir)
		return false
	}
	fmt.Fprintf(ops.out, "ok\n  [3/3] writing %s ... ", outPath)

	args := append([]string{"-czf", outPath, "-C", work, "db.sql", "-C", dir}, relNames(files[1:])...)
	if out, err := ops.run("", "tar", args...); err != nil {
		fmt.Fprintf(ops.out, "FAILED\n\n%s\n", installFirstLine(out))
		return false
	}
	// 0600 AFTER writing: the archive carries .env, which is every secret the box has.
	if err := os.Chmod(outPath, 0o600); err != nil {
		fmt.Fprintf(ops.out, "FAILED\n\ncould not restrict %s to 0600: %v\n", outPath, err)
		return false
	}
	fmt.Fprintf(ops.out, "ok\n\nWrote %s (mode 0600 — it contains your .env).\n", outPath)

	if readEnvValue(filepath.Join(dir, ".env"), "MINIO_REPLICAS") != "0" {
		fmt.Fprintf(ops.out, "\nNOT included: MinIO's object data (attachments). Its volume is larger than a\nconfig backup should silently carry — copy the minio_data volume separately if\nattachments matter to you.\n")
	}
	fmt.Fprintf(ops.out, "\nTo restore onto a fresh box:\n")
	fmt.Fprintf(ops.out, "  tar -xzf %s\n", filepath.Base(outPath))
	fmt.Fprintf(ops.out, "  ptln server install --site <same site>     # then stop: docker compose stop web\n")
	fmt.Fprintf(ops.out, "  cp .env Caddyfile <install-dir>/           # the saved config over the fresh one\n")
	fmt.Fprintf(ops.out, "  cd <install-dir> && docker compose up -d postgres && \\\n")
	fmt.Fprintf(ops.out, "    docker compose exec -T postgres psql -U postgres partyline < db.sql\n")
	fmt.Fprintf(ops.out, "  docker compose up -d\n")
	return true
}

func relNames(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// ── status ──────────────────────────────────────────────────────────────────────────────────────

// serverStatus maps `docker compose ps` into plain language, one line per service, and probes the
// health endpoint. Every bad line carries the command that shows why.
func serverStatus(dir string, ops installOps) bool {
	out, err := ops.run(dir, "docker", "compose", "ps", "-a", "--format", "{{.Service}}\t{{.State}}\t{{.Status}}")
	if err != nil {
		fmt.Fprintf(ops.out, "cannot ask docker about %s: %s\n", dir, installFirstLine(out))
		return false
	}
	healthy := true
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(out) == "" {
		fmt.Fprintf(ops.out, "nothing is running at %s — start it: cd %s && docker compose up -d\n", dir, dir)
		return false
	}
	fmt.Fprintf(ops.out, "install: %s\n\n", dir)
	for _, l := range lines {
		f := strings.SplitN(l, "\t", 3)
		if len(f) < 2 {
			continue
		}
		svc, state := f[0], f[1]
		note := ""
		if len(f) == 3 {
			note = f[2]
		}
		switch {
		case state == "running" && strings.Contains(note, "unhealthy"):
			healthy = false
			fmt.Fprintf(ops.out, "  ✗ %-10s running but failing its healthcheck → docker compose logs %s --tail=50\n", svc, svc)
		case state == "running":
			fmt.Fprintf(ops.out, "  ✓ %-10s running\n", svc)
		case state == "restarting":
			healthy = false
			fmt.Fprintf(ops.out, "  ✗ %-10s crash-looping → docker compose logs %s --tail=50\n", svc, svc)
		case state == "exited" && (svc == "minio-init"):
			fmt.Fprintf(ops.out, "  ✓ %-10s finished (one-shot)\n", svc)
		default:
			healthy = false
			fmt.Fprintf(ops.out, "  ✗ %-10s %s → docker compose logs %s --tail=50\n", svc, state, svc)
		}
	}
	if msg := probeHealth(dir, ops); msg != "" {
		healthy = false
		fmt.Fprintf(ops.out, "\n%s\n", msg)
	} else if readEnvValue(filepath.Join(dir, ".env"), "CADDY_REPLICAS") != "0" {
		fmt.Fprintf(ops.out, "\n  ✓ /api/health answers through the edge\n")
	}
	if healthy {
		fmt.Fprintf(ops.out, "\nHealthy.\n")
	}
	return healthy
}

// runToFileLive streams a command's stdout to a file — for the database dump, which can be larger
// than memory. Stderr is captured small for the error message.
func runToFileLive(dir, path, name string, args ...string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = f
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", err, installFirstLine(errb.String()))
	}
	return nil
}

// serverDay2Main is the shared entry: find the install, run the command.
func serverDay2Main(cmd string, args []string) {
	outPath := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--out" && cmd == "backup" && i+1 < len(args):
			i++
			outPath = args[i]
		case strings.HasPrefix(args[i], "--out=") && cmd == "backup":
			outPath = strings.TrimPrefix(args[i], "--out=")
		case args[i] == "--help" || args[i] == "-h":
			serverUsage(os.Stdout)
			return
		default:
			fmt.Fprintf(os.Stderr, "ptln server %s: unknown argument %q\n", cmd, args[i])
			os.Exit(2)
		}
	}
	dir, err := findInstallDir(os.Stat)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ptln server "+cmd+": "+err.Error())
		os.Exit(1)
	}
	ops := liveInstallOps()
	ok := false
	switch cmd {
	case "upgrade":
		ok = serverUpgrade(dir, ops)
	case "backup":
		ok = serverBackup(dir, outPath, ops)
	case "status":
		ok = serverStatus(dir, ops)
	}
	if !ok {
		os.Exit(1)
	}
}
