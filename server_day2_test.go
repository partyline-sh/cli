package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInstall(t *testing.T, envLines string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"docker-compose.yml": "services: {}\n",
		".env":               envLines,
		"Caddyfile":          "box {\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Upgrade must run in the one order that cannot serve new code against an old schema:
// files → images → containers → migrations.
func TestUpgradeRunsInTheRightOrder(t *testing.T) {
	dir := writeInstall(t, "SITE_URL=https://box.local\nCADDY_REPLICAS=0\n")
	var out bytes.Buffer
	var ran []string
	ops := installOps{
		out: &out,
		run: func(_ string, name string, args ...string) (string, error) {
			ran = append(ran, name+" "+strings.Join(args, " "))
			return "", nil
		},
	}
	if !serverUpgrade(dir, ops) {
		t.Fatalf("upgrade failed:\n%s", out.String())
	}
	joined := strings.Join(ran, " | ")
	pull := strings.Index(joined, "compose pull")
	up := strings.Index(joined, "compose up")
	mig := strings.Index(joined, "apply-migrations")
	if !(pull < up && up < mig) {
		t.Errorf("wrong order: %s", joined)
	}
	// The ticker is gone from the stack; an upgrade must retire the orphan, not leave it
	// ticking beside the web's own at double rate.
	if !strings.Contains(joined, "--remove-orphans") {
		t.Errorf("up must remove orphans: %s", joined)
	}
	// And the stack files must have been refreshed from THIS binary's embed.
	if _, err := os.Stat(filepath.Join(dir, "migrations", "BASELINE")); err != nil {
		t.Error("upgrade did not refresh the embedded schema")
	}
}

// A backup without .env cannot restore the database it contains — the secrets ARE the access.
func TestBackupRefusesWithoutEnv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o600)
	var out bytes.Buffer
	ops := installOps{
		out:       &out,
		run:       func(string, string, ...string) (string, error) { return "", nil },
		runToFile: func(_, path, _ string, _ ...string) error { return os.WriteFile(path, []byte("-- dump"), 0o600) },
	}
	if serverBackup(dir, filepath.Join(t.TempDir(), "b.tar.gz"), ops) {
		t.Fatal("backup should refuse without .env")
	}
	if !strings.Contains(out.String(), "no .env") {
		t.Errorf("should say why:\n%s", out.String())
	}
}

// The archive holds the dump and the config, is 0600 (it contains every secret the box has), and
// the output names the restore steps — a backup nobody knows how to restore is a file, not a backup.
func TestBackupArchiveContentsAndMode(t *testing.T) {
	dir := writeInstall(t, "SITE_URL=https://box.local\nMINIO_REPLICAS=0\n")
	outFile := filepath.Join(t.TempDir(), "b.tar.gz")
	var out bytes.Buffer
	ops := liveInstallOps()
	ops.out = &out
	ops.runToFile = func(_, path, name string, args ...string) error {
		if name != "docker" || !strings.Contains(strings.Join(args, " "), "pg_dump") {
			t.Fatalf("unexpected dump command: %s %v", name, args)
		}
		return os.WriteFile(path, []byte("-- fake dump\n"), 0o600)
	}
	if !serverBackup(dir, outFile, ops) {
		t.Fatalf("backup failed:\n%s", out.String())
	}
	st, err := os.Stat(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("archive holds .env and must be 0600, got %v", st.Mode().Perm())
	}
	listing, err := liveInstallOps().run("", "tar", "-tzf", outFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"db.sql", ".env", "Caddyfile"} {
		if !strings.Contains(listing, want) {
			t.Errorf("archive missing %s: %s", want, listing)
		}
	}
	if !strings.Contains(out.String(), "To restore") {
		t.Errorf("no restore instructions:\n%s", out.String())
	}
	// MINIO_REPLICAS=0 → no attachments warning; there is nothing excluded.
	if strings.Contains(out.String(), "NOT included") {
		t.Errorf("storage is off; nothing was excluded:\n%s", out.String())
	}
}

// Status translates container states and names the command that shows why, per bad line.
func TestStatusNamesTheFailingServiceAndTheFix(t *testing.T) {
	dir := writeInstall(t, "CADDY_REPLICAS=0\n")
	var out bytes.Buffer
	ops := installOps{
		out: &out,
		run: func(_ string, name string, args ...string) (string, error) {
			return "web\trunning\tUp 2 hours\nkeycloak\trestarting\tRestarting\nminio-init\texited\tExited (0)", nil
		},
	}
	if serverStatus(dir, ops) {
		t.Fatal("a crash-looping service is not healthy")
	}
	s := out.String()
	if !strings.Contains(s, "keycloak") || !strings.Contains(s, "crash-looping") {
		t.Errorf("should name the crash loop:\n%s", s)
	}
	if !strings.Contains(s, "docker compose logs keycloak") {
		t.Errorf("every bad line carries the command that shows why:\n%s", s)
	}
	if !strings.Contains(s, "finished (one-shot)") {
		t.Errorf("minio-init exiting is normal and must not read as a failure:\n%s", s)
	}
}

// All three commands refuse plainly when there is no install to act on.
func TestFindInstallDirRefusesPlainly(t *testing.T) {
	t.Setenv("PARTYLINE_DIR", t.TempDir()) // exists, but holds no install
	t.Setenv("HOME", t.TempDir())
	_, err := findInstallDir(os.Stat)
	if err == nil {
		t.Fatal("should refuse with no install")
	}
	if !strings.Contains(err.Error(), "ptln server install") {
		t.Errorf("the refusal should name the way forward: %v", err)
	}
}

// A custom --dir (a real box used /mnt/data/partyline) must be findable by every day-2
// command without PARTYLINE_DIR: the installer records it, findInstallDir reads the record.
func TestFindInstallDirReadsTheRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PARTYLINE_DIR", "")
	t.Setenv("SUDO_USER", "")
	install := filepath.Join(t.TempDir(), "custom-spot")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"docker-compose.yml", ".env"} {
		if err := os.WriteFile(filepath.Join(install, f), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".partyline", "server-dir"), []byte(install+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findInstallDir(os.Stat)
	if err != nil {
		t.Fatalf("findInstallDir: %v", err)
	}
	if got != install {
		t.Errorf("got %q, want the recorded %q", got, install)
	}
}

// Bare `ptln login` on a box that hosts an install must aim at that install's site — the
// operator standing on the hosting box got a lecture instead of a login.
func TestLocalInstanceSite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PARTYLINE_DIR", "")
	t.Setenv("SUDO_USER", "")
	// no install anywhere → ""
	if got := localInstanceSite(); got != "" {
		t.Errorf("no install should mean no site, got %q", got)
	}
	install := filepath.Join(t.TempDir(), "spot")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "docker-compose.yml"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, ".env"), []byte("SITE_URL=https://10.0.0.5:8443/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".partyline", "server-dir"), []byte(install+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := localInstanceSite(); got != "https://10.0.0.5:8443" {
		t.Errorf("localInstanceSite = %q", got)
	}
}
