package main

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// server_install_test.go — the installer's claims, as tests.
//
// The two that matter most are negative: preflight and --dry-run write NOTHING. Both are asserted
// by handing the installer an ops whose every write path fails the test if it is called, so the
// claim does not depend on what the machine running the test happens to have installed.

// refusingOps fails the test on any side effect. Read-only probes are answered from fixtures.
func refusingOps(t *testing.T, out *bytes.Buffer, stdin string) installOps {
	t.Helper()
	return installOps{
		mkdirAll: func(p string, m os.FileMode) error {
			t.Fatalf("wrote: mkdirAll(%q)", p)
			return nil
		},
		writeFile: func(p string, b []byte, m os.FileMode) error {
			t.Fatalf("wrote: writeFile(%q)", p)
			return nil
		},
		run: func(dir, name string, args ...string) (string, error) {
			// docker version probes are read-only and expected; anything else is an action.
			joined := name + " " + strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "docker compose version"):
				return "Docker Compose version v2.29.0", nil
			case strings.HasPrefix(joined, "docker compose ps"):
				return "", nil
			}
			t.Fatalf("ran a command: %s", joined)
			return "", nil
		},
		stat:     os.Stat,
		lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		portBusy: func(string, int) bool { return false },
		httpOK:   func(string) (int, error) { t.Fatal("made a network call"); return 0, nil },
		out:      out,
		// EMPTY STDIN MEANS NO TERMINAL, not "a person who typed nothing". These cases are
		// scripts: they assert preflight, the plan and the write-nothing guarantees, and the
		// setup menu must not engage for them. A test that wants the menu passes input.
		in: func() *bufio.Reader {
			if stdin == "" {
				return nil
			}
			return bufio.NewReader(strings.NewReader(stdin))
		}(),
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	var out bytes.Buffer
	cfg := defaultInstallConfig()
	cfg.dir = t.TempDir() + "/stack"
	cfg.site = "https://box.example.com"
	cfg.dryRun = true

	if ok := runInstall(cfg, refusingOps(t, &out, "")); !ok {
		t.Fatalf("dry run should succeed; got failure. output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Nothing was written") {
		t.Errorf("dry run must say nothing was written; got:\n%s", out.String())
	}
	// And the plan must actually be a plan — naming the steps, not just the config.
	for _, want := range []string{"write the stack files", "write the database schema", "fill in .env", "start the stack"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan is missing step %q; got:\n%s", want, out.String())
		}
	}
}

func TestPreflightFailureWritesNothing(t *testing.T) {
	var out bytes.Buffer
	ops := refusingOps(t, &out, "")
	ops.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	cfg := defaultInstallConfig()
	cfg.dir = t.TempDir() + "/stack"
	cfg.site = "https://box.example.com"

	if ok := runInstall(cfg, ops); ok {
		t.Fatal("install should fail when docker is missing")
	}
	if !strings.Contains(out.String(), "docker is not on PATH") {
		t.Errorf("expected the docker problem to be named; got:\n%s", out.String())
	}
}

func TestBusyPortIsNamedWithItsFlag(t *testing.T) {
	var out bytes.Buffer
	ops := refusingOps(t, &out, "")
	ops.portBusy = func(_ string, port int) bool { return port == 80 }

	cfg := defaultInstallConfig()
	cfg.dir = t.TempDir() + "/stack"
	cfg.site = "https://box.example.com"

	if ok := runInstall(cfg, ops); ok {
		t.Fatal("install should fail when a wanted port is busy")
	}
	// An operator has to be able to act on this: it must name the port AND the flag that moves it.
	if !strings.Contains(out.String(), "port 80") || !strings.Contains(out.String(), "--http-port") {
		t.Errorf("busy-port message must name the port and the flag; got:\n%s", out.String())
	}
}

// A --no-caddy install does not want 80/443 at all, so something already on them is not a problem.
func TestNoCaddySkipsEdgePorts(t *testing.T) {
	var out bytes.Buffer
	ops := refusingOps(t, &out, "")
	ops.portBusy = func(_ string, port int) bool { return port == 80 || port == 443 }

	cfg := defaultInstallConfig()
	cfg.dir = t.TempDir() + "/stack"
	cfg.site = "https://box.example.com"
	cfg.noCaddy = true
	cfg.dryRun = true

	if ok := runInstall(cfg, ops); !ok {
		t.Fatalf("--no-caddy must not care about 80/443; got:\n%s", out.String())
	}
}

func TestSiteRequiredWithYes(t *testing.T) {
	var out bytes.Buffer
	cfg := defaultInstallConfig()
	cfg.dir = t.TempDir() + "/stack"
	cfg.assumeYes = true

	if ok := runInstall(cfg, refusingOps(t, &out, "")); ok {
		t.Fatal("--yes without --site must fail rather than guess a URL")
	}
	if !strings.Contains(out.String(), "--site is required") {
		t.Errorf("expected the missing --site to be named; got:\n%s", out.String())
	}
}

func TestNormalizeSiteURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://a.example.com", "https://a.example.com"},
		{"https://a.example.com/", "https://a.example.com"},
		{"http://a.example.com", "http://a.example.com"},
		// A bare hostname is what people type. It is completed, not rejected.
		{"a.example.com", "https://a.example.com"},
	}
	for _, c := range cases {
		got, err := normalizeSiteURL(c.in)
		if err != nil {
			t.Errorf("normalizeSiteURL(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeSiteURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "   ", "ftp://a.example.com", "https://"} {
		if _, err := normalizeSiteURL(bad); err == nil {
			t.Errorf("normalizeSiteURL(%q) should have failed", bad)
		}
	}
}

// The embedded stack must actually contain the files the plan promises to write, or the installer
// is shipping a broken promise that only shows up on a real box.
func TestEmbeddedStackHasRequiredFiles(t *testing.T) {
	assets, err := stackAssets()
	if err != nil {
		t.Fatalf("stackAssets: %v", err)
	}
	have := map[string]bool{}
	for _, a := range assets {
		have[a] = true
	}
	for _, f := range append([]string{"Caddyfile.prod", "init/00-bootstrap.sh"}, stackFiles...) {
		if !have[f] {
			t.Errorf("the embedded stack is missing %s (have: %v)", f, assets)
		}
	}
}

// apply-migrations.sh fails rather than "succeeding" against an empty directory, so the installer
// must actually carry the schema. An empty embed is a build bug, not a runtime condition.
func TestEmbeddedMigrationsAreCarried(t *testing.T) {
	dir := t.TempDir()
	n, err := writeMigrations(dir)
	if err != nil {
		t.Fatalf("writeMigrations: %v", err)
	}
	if n < 100 {
		t.Errorf("only %d migrations were embedded; the schema is ~176 files", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "migrations", "BASELINE")); err != nil {
		t.Errorf("BASELINE did not land: %v", err)
	}
}

func TestWriteStackIsIdempotentAndExecutable(t *testing.T) {
	dir := t.TempDir()

	first, err := writeStack(dir)
	if err != nil {
		t.Fatalf("writeStack: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("first writeStack wrote nothing")
	}
	// A shipped .sh that lands 0644 makes the first ./apply-migrations.sh "permission denied".
	st, err := os.Stat(filepath.Join(dir, "apply-migrations.sh"))
	if err != nil {
		t.Fatalf("stat apply-migrations.sh: %v", err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("apply-migrations.sh is not executable: %v", st.Mode())
	}

	second, err := writeStack(dir)
	if err != nil {
		t.Fatalf("second writeStack: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("re-running writeStack rewrote %v; it must be a no-op when content matches", second)
	}
}

// .env holds the box's secrets. An install that rewrites it turns a re-run into an outage.
func TestEnvOverridesOnlyAppendMissingNames(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	// HTTP_PORT is already set to something the operator chose. It must survive.
	if err := os.WriteFile(envPath, []byte("SITE_URL=https://a.example.com\nHTTP_PORT=8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := defaultInstallConfig()
	cfg.dir = dir
	ops := installOps{writeFile: os.WriteFile}
	if err := writeInstallEnvOverrides(cfg, ops); err != nil {
		t.Fatalf("writeInstallEnvOverrides: %v", err)
	}

	body, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if strings.Contains(got, "HTTP_PORT=80\n") {
		t.Errorf("an existing HTTP_PORT was overwritten:\n%s", got)
	}
	if !strings.Contains(got, "HTTP_PORT=8080") {
		t.Errorf("the operator's HTTP_PORT did not survive:\n%s", got)
	}
	for _, want := range []string{"BIND_ADDR=", "HTTPS_PORT=", "RELAY_PORT="} {
		if !strings.Contains(got, want) {
			t.Errorf("missing override %s:\n%s", want, got)
		}
	}
}

// The bug a real box found: env-bootstrap.sh writes HTTP_PORT=80 itself, so an override that only
// appended-when-missing silently dropped the operator's --http-port. The plan said 8880 and Caddy
// bound 80. An explicitly passed flag must win over a value already in .env.
func TestExplicitPortOverwritesExistingEnvValue(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("SITE_URL=https://a.example.com\nHTTP_PORT=80\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := defaultInstallConfig()
	cfg.dir = dir
	cfg.httpPort = 8880
	cfg.explicit["HTTP_PORT"] = true

	if err := writeInstallEnvOverrides(cfg, installOps{writeFile: os.WriteFile}); err != nil {
		t.Fatalf("writeInstallEnvOverrides: %v", err)
	}
	body, _ := os.ReadFile(envPath)
	got := string(body)
	if !strings.Contains(got, "HTTP_PORT=8880") {
		t.Errorf("explicit --http-port did not reach .env:\n%s", got)
	}
	if strings.Contains(got, "HTTP_PORT=80\n") {
		t.Errorf("the old HTTP_PORT survived alongside the new one:\n%s", got)
	}
	// Exactly one HTTP_PORT line, or compose reads the wrong one.
	if n := strings.Count(got, "HTTP_PORT="); n != 1 {
		t.Errorf("want exactly 1 HTTP_PORT line, got %d:\n%s", n, got)
	}
}

// ...but a DEFAULT must never overwrite what is already there. Only an explicit flag wins.
func TestDefaultPortDoesNotOverwriteExistingEnvValue(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("HTTP_PORT=8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := defaultInstallConfig() // httpPort 80, not explicit
	cfg.dir = dir

	if err := writeInstallEnvOverrides(cfg, installOps{writeFile: os.WriteFile}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(envPath)
	if !strings.Contains(string(body), "HTTP_PORT=8080") {
		t.Errorf("a default clobbered the operator's existing HTTP_PORT:\n%s", string(body))
	}
}

func TestInstallLocalBaseDoesNotUseSiteURL(t *testing.T) {
	cfg := defaultInstallConfig()
	// The public URL may not resolve on the box itself. Verify must not depend on it.
	cfg.site = "https://not-resolvable.example.com"
	cfg.httpPort = 8080
	if got := installLocalBase(cfg); strings.Contains(got, "not-resolvable") {
		t.Errorf("verify used the public URL (%q); it must dial this box directly", got)
	} else if got != "https://127.0.0.1:443" {
		t.Errorf("installLocalBase = %q, want https://127.0.0.1:443", got)
	}
}

// Most self-hosted boxes have no public domain. That must be a working path, not a warning.
func TestTLSModeChosenFromTheName(t *testing.T) {
	cases := []struct {
		site string
		want tlsMode
	}{
		// Public names: a real certificate is possible.
		{"https://partyline.example.com", tlsACME},
		{"https://pl.acme.co.uk", tlsACME},
		// No public CA can issue for any of these. They are the common self-host case.
		{"https://192.168.1.50", tlsInternal},
		{"https://10.0.0.4", tlsInternal},
		{"https://monolith", tlsInternal},
		{"https://box.local", tlsInternal},
		{"https://pl.monolith.test", tlsInternal},
		{"https://nuc.home.arpa", tlsInternal},
		{"https://box.tail1234.ts.net", tlsInternal},
		{"https://localhost", tlsInternal},
		// An explicit http:// site means the operator does not want TLS at all.
		{"http://192.168.1.50", tlsOff},
	}
	for _, c := range cases {
		if got := resolveTLSMode(tlsAuto, c.site); got != c.want {
			t.Errorf("resolveTLSMode(auto, %q) = %q, want %q", c.site, got, c.want)
		}
	}
	// An explicit flag always wins over the guess.
	if got := resolveTLSMode(tlsACME, "https://192.168.1.50"); got != tlsACME {
		t.Errorf("an explicit --tls acme must win, got %q", got)
	}
	if got := resolveTLSMode(tlsInternal, "https://real.example.com"); got != tlsInternal {
		t.Errorf("an explicit --tls internal must win, got %q", got)
	}
}

// The generated edge must actually name the operator's host. Copying Caddyfile.prod shipped a
// config whose only site block was partyline.sh, so a real box matched nothing.
func TestCaddyfileNamesTheOperatorsHost(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://box.local"
	cfg.httpPort = 8880
	cfg.httpsPort = 8443

	got := caddyfileFor(cfg)
	if !strings.Contains(got, "box.local {") {
		t.Errorf("Caddyfile does not open a site block for the operator's host:\n%s", got)
	}
	if strings.Contains(got, "partyline.sh") {
		t.Errorf("Caddyfile still names partyline.sh:\n%s", got)
	}
	// A no-public-domain name gets Caddy's own CA, so HTTPS works with no internet at all.
	if !strings.Contains(got, "tls internal") {
		t.Errorf("an internal name should get `tls internal`:\n%s", got)
	}
	// Caddy runs INSIDE a container whose ports compose maps as ${HTTP_PORT}:80 and
	// ${HTTPS_PORT}:443 — the container side is fixed. Setting http_port/https_port to the HOST
	// ports moved Caddy's listeners off 80/443 and the mapping then forwarded to nothing: a
	// valid certificate and a total timeout. It must not carry them.
	if strings.Contains(got, "http_port") || strings.Contains(got, "https_port") {
		t.Errorf("Caddyfile overrides its listener ports; compose maps the container side:\n%s", got)
	}
	// And it must still route every surface. keycloak:8080 is load-bearing: /auth is where the
	// OIDC issuer lives, so without it discovery reaches the Next.js app and sign-in is
	// impossible on a box the installer built.
	for _, want := range []string{"postgrest:3000", "keycloak:8080", "web:3000"} {
		if !strings.Contains(got, want) {
			t.Errorf("Caddyfile lost the %s route:\n%s", want, got)
		}
	}
}

func TestCaddyfileACMEAndOffModes(t *testing.T) {
	acme := defaultInstallConfig()
	acme.site = "https://real.example.com"
	if got := caddyfileFor(acme); strings.Contains(got, "tls internal") {
		t.Errorf("a public name must not be pinned to the internal CA:\n%s", got)
	}

	off := defaultInstallConfig()
	off.site = "http://192.168.1.50"
	got := caddyfileFor(off)
	if !strings.Contains(got, "auto_https off") {
		t.Errorf("--tls off must disable automatic HTTPS:\n%s", got)
	}
	if !strings.Contains(got, "http://192.168.1.50 {") {
		t.Errorf("plain-HTTP site block missing:\n%s", got)
	}
}

// A bare hostname or an IP is a legitimate --site. It must not be rejected as "not a URL".
func TestSiteAcceptsHostsWithoutADomain(t *testing.T) {
	for _, in := range []string{"192.168.1.50", "monolith", "http://10.0.0.4", "box.local"} {
		if _, err := normalizeSiteURL(in); err != nil {
			t.Errorf("normalizeSiteURL(%q) rejected a legitimate self-host address: %v", in, err)
		}
	}
}

// A "denied" pulling a PUBLIC image is a stale local credential, not missing access. Saying so is
// the difference between a 30-second fix and asking for permissions you already have.
func TestPullHintExplainsStaleLogin(t *testing.T) {
	out := "Image ghcr.io/partyline-sh/partyline-relay:latest Error error from registry: denied"
	hint := pullHint(out)
	// The public registry is Docker Hub. Telling someone their GHCR login is stale, when the
	// package was never readable anonymously, sends them to debug the wrong thing entirely.
	if !strings.Contains(hint, "DOCKER HUB") {
		t.Errorf("a denial should name the registry that actually works; got %q", hint)
	}
	if !strings.Contains(hint, "WEB_IMAGE=docker.io/partyline/partyline-web") {
		t.Errorf("a ghcr.io denial should give the override that fixes it; got %q", hint)
	}
	if !strings.Contains(hint, "docker logout ghcr.io") {
		t.Errorf("the stale-credential case should still be covered; got %q", hint)
	}
	if pullHint("no such host") != "" {
		t.Error("an unrelated pull failure must not get the stale-login hint")
	}
}

// Connecting to an IP address sends no SNI, so Caddy cannot pick a certificate and the TLS
// handshake fails outright — HTTP redirects, HTTPS answers nothing. A bare IP is a completely
// ordinary --site for a self-hosted box, so it has to work.
func TestCaddyfileSetsDefaultSNIForBareIP(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://192.168.1.50"
	got := caddyfileFor(cfg)
	if !strings.Contains(got, "default_sni 192.168.1.50") {
		t.Errorf("an IP site needs default_sni or TLS never completes:\n%s", got)
	}

	// A NAME carries SNI, so it must not get a default — that would pin the wrong cert.
	named := defaultInstallConfig()
	named.site = "https://box.local"
	if strings.Contains(caddyfileFor(named), "default_sni") {
		t.Errorf("a named site should not need default_sni:\n%s", caddyfileFor(named))
	}
}

// The last thing an operator reads is the report, so a command named there has to be real. It
// said `ptln login --api <url>`; the actual form is `ptln login <url>`, and no such flag exists.
func TestReportNamesRealCommands(t *testing.T) {
	var out bytes.Buffer
	cfg := defaultInstallConfig()
	cfg.site = "https://box.local"
	cfg.dir = "/opt/partyline"
	printInstallReport(cfg, installOps{out: &out})
	got := out.String()

	if !strings.Contains(got, "ptln login https://box.local") {
		t.Errorf("report should point the CLI at this box with `ptln login <url>`:\n%s", got)
	}
	// Guard the specific mistake: a flag the command does not have.
	if strings.Contains(got, "--api") {
		t.Errorf("report names a --api flag that ptln login does not have:\n%s", got)
	}
}

// The web container reaches PostgREST through Caddy's INTERNAL door. env-bootstrap.sh derives
// PGRST_URL from SITE_URL, which points the container at itself: every server-side query fails,
// including the device-code sign-in a CLI needs.
func TestPgrstUrlUsesTheInternalDoor(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	// Exactly what env-bootstrap.sh writes.
	if err := os.WriteFile(envPath, []byte("SITE_URL=https://box.local\nPGRST_URL=https://box.local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultInstallConfig()
	cfg.dir = dir
	cfg.site = "https://box.local"

	var out bytes.Buffer
	if err := writeInstallEnvOverrides(cfg, installOps{writeFile: os.WriteFile, out: &out}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(envPath)
	if !strings.Contains(string(got), "PGRST_URL=http://caddy") {
		t.Errorf("PGRST_URL was not repaired to the internal door:\n%s", string(got))
	}
	if strings.Contains(string(got), "PGRST_URL=https://box.local") {
		t.Errorf("the self-referential PGRST_URL survived:\n%s", string(got))
	}
}

// ...but a PGRST_URL the operator actually chose is theirs, and must not be rewritten.
func TestPgrstUrlLeavesADeliberateValueAlone(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("SITE_URL=https://box.local\nPGRST_URL=https://db.elsewhere.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultInstallConfig()
	cfg.dir = dir
	cfg.site = "https://box.local"

	var out bytes.Buffer
	if err := writeInstallEnvOverrides(cfg, installOps{writeFile: os.WriteFile, out: &out}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(envPath)
	if !strings.Contains(string(got), "PGRST_URL=https://db.elsewhere.example") {
		t.Errorf("an operator's own PGRST_URL was overwritten:\n%s", string(got))
	}
}

// With no edge there is no internal door, so PGRST_URL must not be pointed at one.
func TestNoCaddySkipsTheInternalDoor(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultInstallConfig()
	cfg.dir = dir
	cfg.site = "https://box.local"
	cfg.noCaddy = true

	var out bytes.Buffer
	if err := writeInstallEnvOverrides(cfg, installOps{writeFile: os.WriteFile, out: &out}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Contains(string(got), "http://caddy") {
		t.Errorf("--no-caddy must not route through an edge that is not running:\n%s", string(got))
	}
}

// Both doors must serve the same routes, or the app and the browser see different applications.
func TestCaddyfileHasAnInternalDoor(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://box.local"
	got := caddyfileFor(cfg)
	if !strings.Contains(got, "http://caddy {") {
		t.Errorf("no internal site block; the app cannot reach PostgREST:\n%s", got)
	}
	if n := strings.Count(got, "reverse_proxy postgrest:3000"); n != 2 {
		t.Errorf("want the PostgREST route on both doors, got %d:\n%s", n, got)
	}
	if n := strings.Count(got, "reverse_proxy web:3000"); n != 2 {
		t.Errorf("want the web route on both doors, got %d:\n%s", n, got)
	}
	if n := strings.Count(got, "reverse_proxy keycloak:8080"); n != 2 {
		t.Errorf("want the Keycloak route on both doors, got %d:\n%s", n, got)
	}
}

// SITE_URL is what the app builds its own links from. On a remapped-port box a --site with no port
// produces a sign-in URL that does not resolve — observed: device/start returned
// "https://127.0.0.1/activate?..." on a box answering only on :8443.
func TestPlanWarnsWhenSiteOmitsARemappedPort(t *testing.T) {
	var out bytes.Buffer
	cfg := defaultInstallConfig()
	cfg.dir = t.TempDir() + "/stack"
	cfg.site = "https://box.local"
	cfg.httpsPort = 8443
	cfg.dryRun = true

	if ok := runInstall(cfg, refusingOps(t, &out, "")); !ok {
		t.Fatalf("dry run failed:\n%s", out.String())
	}
	got := out.String()
	if !strings.Contains(got, "--site https://box.local:8443") {
		t.Errorf("plan should tell the operator to put the port in --site:\n%s", got)
	}

	// A default-port install has nothing to warn about.
	var quiet bytes.Buffer
	plain := defaultInstallConfig()
	plain.dir = t.TempDir() + "/stack"
	plain.site = "https://box.local"
	plain.dryRun = true
	if ok := runInstall(plain, refusingOps(t, &quiet, "")); !ok {
		t.Fatalf("dry run failed:\n%s", quiet.String())
	}
	if strings.Contains(quiet.String(), "names no port") {
		t.Errorf("a default-port install should not warn:\n%s", quiet.String())
	}
}

// A Caddy site address WITH a port means "listen on that port" — the same listener mistake that
// made a valid certificate serve nothing. The port belongs in SITE_URL, not the site block.
func TestCaddyfileStripsThePortFromTheSiteAddress(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://box.local:8443"
	cfg.httpsPort = 8443
	got := caddyfileFor(cfg)
	if !strings.Contains(got, "box.local {") {
		t.Errorf("site block should name the host without its port:\n%s", got)
	}
	if strings.Contains(got, "box.local:8443 {") {
		t.Errorf("a ported site address makes Caddy listen on that port:\n%s", got)
	}
}

// A loopback --site installs cleanly, serves pages, passes the health check, and can never sign
// anyone in: the web container uses SITE_URL to reach the identity provider, and inside a container
// 127.0.0.1 is the container. Verified on a real box — discovery failed with ECONNREFUSED.
func TestLoopbackSiteIsRefused(t *testing.T) {
	for _, bad := range []string{"https://127.0.0.1:8443", "http://localhost:8880", "https://[::1]"} {
		var out bytes.Buffer
		cfg := defaultInstallConfig()
		cfg.dir = t.TempDir() + "/stack"
		cfg.site = bad
		cfg.assumeYes = true
		if ok := runInstall(cfg, refusingOps(t, &out, "")); ok {
			t.Errorf("--site %s should be refused", bad)
		}
		if !strings.Contains(out.String(), "only works from this machine") {
			t.Errorf("--site %s: expected the loopback explanation, got:\n%s", bad, out.String())
		}
	}
	// A routable address is fine.
	var out bytes.Buffer
	cfg := defaultInstallConfig()
	cfg.dir = t.TempDir() + "/stack"
	cfg.site = "http://192.168.1.50:8880"
	cfg.dryRun = true
	if ok := runInstall(cfg, refusingOps(t, &out, "")); !ok {
		t.Errorf("a LAN address must be accepted:\n%s", out.String())
	}
}

// The default used to be /opt/partyline unconditionally, which turned a first install into a
// permissions problem and then sent the operator to `sudo ptln` — a command that does not resolve
// when ptln lives in ~/.local/bin. Both halves are fixed; this pins the directory half.
func TestDefaultInstallDirFallsBackToSomewhereYouOwn(t *testing.T) {
	// /opt writable: keep the familiar location.
	if got := defaultInstallDirFor("/home/someone", func(string) bool { return true }); got != defaultInstallDir {
		t.Errorf("with a writable /opt, want %s, got %s", defaultInstallDir, got)
	}
	// /opt not writable: a directory the operator already owns, with no privilege needed.
	if got := defaultInstallDirFor("/home/someone", func(string) bool { return false }); got != "/home/someone/partyline" {
		t.Errorf("with an unwritable /opt, want /home/someone/partyline, got %s", got)
	}
	// No home to fall back on — better the FHS location than an empty path.
	if got := defaultInstallDirFor("", func(string) bool { return false }); got != defaultInstallDir {
		t.Errorf("with no home, want %s, got %s", defaultInstallDir, got)
	}
}

// The advice has to name a command that exists. `sudo ptln` does not when ptln is off root's PATH.
func TestSudoHintNamesAnAbsolutePath(t *testing.T) {
	got := sudoHint("server install")
	if !strings.HasPrefix(got, "sudo /") {
		t.Errorf("sudo hint must use an absolute path so it resolves under sudo; got %q", got)
	}
	if !strings.HasSuffix(got, " server install") {
		t.Errorf("sudo hint lost its arguments; got %q", got)
	}
}

// A busy port used to end the install: "pass --http-port to move it, or stop what is there".
// On a box that already runs something — most boxes — the first install could not succeed without
// guessing a free port and retyping the command. It asks instead.
func TestPortWizardOffersAFreePortAndTakesTheAnswer(t *testing.T) {
	var out bytes.Buffer
	busy := map[int]bool{80: true, 443: true}
	ops := installOps{
		out:      &out,
		in:       bufio.NewReader(strings.NewReader("\n9443\n")), // accept 8080, then type 9443
		portBusy: func(_ string, p int) bool { return busy[p] },
		run: func(_, name string, _ ...string) (string, error) {
			if name == "ss" {
				return `LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=812,fd=6))`, nil
			}
			return "", nil
		},
	}
	cfg := defaultInstallConfig()
	got, ok := resolvePortConflicts(cfg, ops)
	if !ok {
		t.Fatal("wizard should complete when it can ask")
	}
	if got.httpPort != 8080 {
		t.Errorf("pressing enter should take the suggestion, got %d", got.httpPort)
	}
	if got.httpsPort != 9443 {
		t.Errorf("a typed port should be used, got %d", got.httpsPort)
	}
	if got.relayPort != 2222 {
		t.Errorf("an unaffected port should not move, got %d", got.relayPort)
	}
	// It has to name what is holding the port, or the operator cannot decide whether to move.
	if !strings.Contains(out.String(), "nginx") {
		t.Errorf("wizard should name the listener:\n%s", out.String())
	}
	// The choices must reach .env, which only happens for ports marked explicit.
	for _, k := range []string{"HTTP_PORT", "HTTPS_PORT"} {
		if !got.explicit[k] {
			t.Errorf("%s was chosen interactively but not marked explicit, so it would not be written", k)
		}
	}
}

// --yes has nobody to ask. Silently moving a service to a port the caller did not choose is worse
// than stopping, so it still reports — but it names a port that is actually free.
func TestPortConflictUnderYesReportsAFreePort(t *testing.T) {
	var out bytes.Buffer
	ops := refusingOps(t, &out, "")
	ops.portBusy = func(_ string, p int) bool { return p == 80 }

	cfg := defaultInstallConfig()
	cfg.dir = t.TempDir() + "/stack"
	cfg.site = "https://box.local"
	cfg.assumeYes = true

	if ok := runInstall(cfg, ops); ok {
		t.Fatal("a busy port with --yes should still stop")
	}
	if !strings.Contains(out.String(), "--http-port") {
		t.Errorf("should name the flag:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "is free") {
		t.Errorf("should suggest a port that is actually free:\n%s", out.String())
	}
}

func TestListenerParsedFromSs(t *testing.T) {
	out := `State  Recv-Q Send-Q Local Address:Port Peer Address:Port
LISTEN 0      511          0.0.0.0:80        0.0.0.0:*     users:(("nginx",pid=812,fd=6))
LISTEN 0      128             [::]:22           [::]:*     users:(("sshd",pid=901,fd=4))`
	if got := matchListener(out, 80); got != "nginx" {
		t.Errorf("port 80 should be nginx, got %q", got)
	}
	if got := matchListener(out, 22); got != "sshd" {
		t.Errorf("port 22 should be sshd (IPv6 form), got %q", got)
	}
	if got := matchListener(out, 8080); got != "" {
		t.Errorf("an unlisted port should return empty, got %q", got)
	}
}

// The menu must run BEFORE preflight. The previous attempt sat after it, so preflight returned
// first and the prompt was dead code — an operator got "pass --http-port (8080 is free)" and a
// refusal, which is the same wall with a better sign on it.
func TestMenuRunsBeforePreflightAndResolvesConflicts(t *testing.T) {
	var out bytes.Buffer
	busy := map[int]bool{80: true, 443: true}
	ops := installOps{
		out: &out,
		// change http → 8080, change https → 8443, then install
		in:       bufio.NewReader(strings.NewReader("4\n8080\n5\n8443\n\n")),
		portBusy: func(_ string, p int) bool { return busy[p] },
		run:      func(string, string, ...string) (string, error) { return "", nil },
	}
	cfg := defaultInstallConfig()
	cfg.site = "http://192.168.1.50:8080"

	got, ok := runSetupMenu(cfg, ops)
	if !ok {
		t.Fatalf("menu should complete:\n%s", out.String())
	}
	if got.httpPort != 8080 || got.httpsPort != 8443 {
		t.Errorf("ports not applied: http=%d https=%d", got.httpPort, got.httpsPort)
	}
	// The conflict has to be visible on the line, not just in a summary.
	if !strings.Contains(out.String(), "IN USE") {
		t.Errorf("menu should mark the busy ports:\n%s", out.String())
	}
	// Interactive choices must be marked explicit or env-bootstrap.sh overwrites them.
	for _, k := range []string{"HTTP_PORT", "HTTPS_PORT"} {
		if !got.explicit[k] {
			t.Errorf("%s chosen in the menu but not explicit — it would not reach .env", k)
		}
	}
}

// Enter must not install while something is unresolved; the menu says what and loops.
func TestMenuRefusesToInstallWithABusyPort(t *testing.T) {
	var out bytes.Buffer
	ops := installOps{
		out:      &out,
		in:       bufio.NewReader(strings.NewReader("\nq\n")), // try to install, then quit
		portBusy: func(_ string, p int) bool { return p == 80 },
		run:      func(string, string, ...string) (string, error) { return "", nil },
	}
	cfg := defaultInstallConfig()
	cfg.site = "https://box.local"

	if _, ok := runSetupMenu(cfg, ops); ok {
		t.Error("pressing enter with a busy port should not proceed")
	}
	if !strings.Contains(out.String(), "8080 is free") {
		t.Errorf("should name a free port:\n%s", out.String())
	}
}

// A missing site blocks install and is named as the first thing to fix.
func TestMenuRequiresASite(t *testing.T) {
	var out bytes.Buffer
	ops := installOps{
		out:      &out,
		in:       bufio.NewReader(strings.NewReader("\nq\n")),
		portBusy: func(string, int) bool { return false },
		run:      func(string, string, ...string) (string, error) { return "", nil },
	}
	cfg := defaultInstallConfig()

	if _, ok := runSetupMenu(cfg, ops); ok {
		t.Error("should not install with no site set")
	}
	if !strings.Contains(out.String(), "Set the site address first") {
		t.Errorf("should name the site as the blocker:\n%s", out.String())
	}
}

// --yes has nobody to ask: the menu is skipped entirely and the flags are the answer.
func TestMenuSkippedUnderYes(t *testing.T) {
	var out bytes.Buffer
	ops := installOps{out: &out, in: nil, portBusy: func(string, int) bool { return true }}
	cfg := defaultInstallConfig()
	cfg.assumeYes = true
	cfg.site = "https://box.local"

	got, ok := runSetupMenu(cfg, ops)
	if !ok {
		t.Error("--yes should pass straight through")
	}
	if got.httpPort != cfg.httpPort || out.String() != "" {
		t.Errorf("--yes should neither prompt nor change anything: %q", out.String())
	}
}

// The site name is the one setting nothing downstream can recover from: SITE_URL is what every
// link, every token issuer and the container's own OIDC discovery are built from. A name that
// resolves nowhere installs cleanly and fails at sign-in.
func TestSiteDNSIsChecked(t *testing.T) {
	local := []string{"100.79.11.16", "192.168.1.170"}

	// Points here.
	r, _ := checkSiteDNS("https://box.example.com",
		func(string) ([]string, error) { return []string{"192.168.1.170"}, nil }, local)
	if r != dnsResolvesHere {
		t.Errorf("a record pointing at one of our addresses should read as here, got %v", r)
	}

	// Points somewhere else — stale record, or a proxy. Reported, not blocked.
	r, addrs := checkSiteDNS("https://box.example.com",
		func(string) ([]string, error) { return []string{"203.0.113.9"}, nil }, local)
	if r != dnsResolvesElsewhere {
		t.Errorf("a foreign address should read as elsewhere, got %v", r)
	}
	if !strings.Contains(dnsNote(r, addrs), "203.0.113.9") {
		t.Error("the note should name what it actually resolved to")
	}

	// No record.
	r, _ = checkSiteDNS("https://box.example.com",
		func(string) ([]string, error) { return nil, errors.New("no such host") }, local)
	if r != dnsMissing {
		t.Errorf("no record should read as missing, got %v", r)
	}

	// An IP literal is its own answer — there is nothing to look up.
	r, _ = checkSiteDNS("http://192.168.1.50:8080",
		func(string) ([]string, error) { t.Fatal("must not resolve an IP literal"); return nil, nil }, local)
	if r != dnsNotChecked {
		t.Errorf("an IP site needs no lookup, got %v", r)
	}
}

// The answer to "how many records" is ONE, and the installer has to say so — an operator who
// thinks a self-hosted instance needs a zone full of entries either over-builds or puts it off.
func TestDNSAdviceNamesOneRecord(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://partyline.example.com"
	got := dnsAdvice(cfg, []string{"203.0.113.10"})

	if !strings.Contains(got, "One DNS record") {
		t.Errorf("should say how many:\n%s", got)
	}
	if !strings.Contains(got, "partyline.example.com") || !strings.Contains(got, "203.0.113.10") {
		t.Errorf("should name the host and the target:\n%s", got)
	}
	if !strings.Contains(got, "relay does not need its own name") {
		t.Errorf("should rule out the second record people assume they need:\n%s", got)
	}
	// An IP site needs no DNS at all.
	cfg.site = "http://192.168.1.50:8080"
	if dnsAdvice(cfg, []string{"192.168.1.50"}) != "" {
		t.Error("an IP address needs no DNS advice")
	}
}

// A custom resolver is optional, and it has to reach the CONTAINERS — the web container resolves
// SITE_URL itself to fetch OIDC discovery, so a name the host can see and a container cannot is an
// install that passes every step and cannot sign anybody in.
func TestCustomResolverIsOptionalAndReachesTheContainers(t *testing.T) {
	// Unset: the system resolver is used unchanged.
	called := false
	fallback := func(string) ([]string, error) { called = true; return []string{"1.2.3.4"}, nil }
	if _, err := resolverFor("", fallback)("x"); err != nil || !called {
		t.Error("an empty dns setting must use the system resolver")
	}

	// Set: the override reaches every service that resolves a name.
	y := dnsOverrideYAML("10.0.0.53")
	for _, svc := range []string{"web:", "caddy:", "relay:"} {
		if !strings.Contains(y, svc) {
			t.Errorf("override is missing %s:\n%s", svc, y)
		}
	}
	if strings.Count(y, "10.0.0.53") != 3 {
		t.Errorf("every service should get the resolver:\n%s", y)
	}
}

// The override must be REMOVED when no resolver is set, not left behind from a previous run.
func TestResolverOverrideRemovedWhenUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.override.yml")
	if err := os.WriteFile(path, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := defaultInstallConfig()
	cfg.dir = dir // dns unset

	var step installStep
	for _, s := range installSteps(cfg) {
		if strings.Contains(s.what, "resolver") {
			step = s
		}
	}
	if step.do == nil {
		t.Fatal("no resolver step found")
	}
	ops := installOps{writeFile: os.WriteFile, stat: os.Stat}
	if err := step.do(cfg, ops); err != nil {
		t.Fatalf("step: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a stale override from a previous run should be removed, not left overriding")
	}
	if err := step.check(cfg, ops); err != nil {
		t.Errorf("check should pass with no resolver: %v", err)
	}
}

// A rejected value used to be printed below the frame and wiped by the next paint's screen-clear
// after one visible frame — the flash read as the menu ignoring the keystroke. The message must
// appear INSIDE a frame painted after the rejection.
func TestMenuStatusSurvivesTheRepaint(t *testing.T) {
	var out bytes.Buffer
	ops := installOps{
		out:      &out,
		in:       bufio.NewReader(strings.NewReader("4\nnotaport\nq\n")),
		portBusy: func(string, int) bool { return false },
		run:      func(string, string, ...string) (string, error) { return "", nil },
	}
	cfg := defaultInstallConfig()
	cfg.site = "http://192.168.1.50:8080"
	runSetupMenu(cfg, ops)

	s := out.String()
	last := strings.LastIndex(s, "\x1b[2J") // the final frame
	if last < 0 {
		t.Fatal("no frame painted")
	}
	if !strings.Contains(s[last:], "not a port number") {
		t.Errorf("the rejection must be inside the final frame, not flashed before it:\n%q", s[last:])
	}
}

// DNS propagation is measured in minutes; a one-shot check made the operator reopen the screen to
// learn whether their record had landed. The watch polls until it resolves.
func TestWatchSiteDNSComesBackWhenTheRecordLands(t *testing.T) {
	var out bytes.Buffer
	misses := 0
	slept := 0
	ops := installOps{
		out: &out,
		lookup: func(string) ([]string, error) {
			if misses < 3 {
				misses++
				return nil, errors.New("NXDOMAIN")
			}
			return []string{"192.168.1.170"}, nil
		},
		localIPs: func() []string { return []string{"192.168.1.170"} },
		sleep:    func(time.Duration) { slept++ },
	}
	cfg := defaultInstallConfig()
	cfg.site = "https://pl.example.com"

	got := watchSiteDNS(cfg, ops)
	if !strings.Contains(got, "resolved") || !strings.Contains(got, "points at this box") {
		t.Errorf("watch should report the record landing, got %q", got)
	}
	if slept != 3 {
		t.Errorf("should have waited once per miss, slept %d times for %d misses", slept, misses)
	}
	// The wait has to be visibly alive — a dot per miss.
	if strings.Count(out.String(), ".") < 3 {
		t.Errorf("no heartbeat during the watch:\n%q", out.String())
	}
}

// A record pointing somewhere else ends the watch too — waiting five minutes to repeat a fact the
// first check established would be theatre.
func TestWatchSiteDNSStopsOnAForeignAnswer(t *testing.T) {
	var out bytes.Buffer
	ops := installOps{
		out:      &out,
		lookup:   func(string) ([]string, error) { return []string{"203.0.113.9"}, nil },
		localIPs: func() []string { return []string{"192.168.1.170"} },
		sleep:    func(time.Duration) { t.Fatal("a resolving name needs no wait") },
	}
	cfg := defaultInstallConfig()
	cfg.site = "https://pl.example.com"
	got := watchSiteDNS(cfg, ops)
	if !strings.Contains(got, "203.0.113.9") || !strings.Contains(got, "NOT this box") {
		t.Errorf("should name where it went, got %q", got)
	}
}

func TestWatchSiteDNSGivesUpWithBudget(t *testing.T) {
	var out bytes.Buffer
	ops := installOps{
		out:      &out,
		lookup:   func(string) ([]string, error) { return nil, errors.New("NXDOMAIN") },
		localIPs: func() []string { return nil },
		sleep:    func(time.Duration) {},
	}
	cfg := defaultInstallConfig()
	cfg.site = "https://pl.example.com"
	got := watchSiteDNS(cfg, ops)
	if !strings.Contains(got, "still nothing") {
		t.Errorf("an exhausted watch must say so, got %q", got)
	}
}

// The advice offers BOTH shapes — A for an address, CNAME to alias a name that already points
// here — because "do I make an A or a CNAME" is the actual question at the registrar.
func TestDNSAdviceOffersARecordOrCNAME(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://partyline.example.com"
	got := dnsAdvice(cfg, []string{"203.0.113.10"})
	if !strings.Contains(got, " A ") && !strings.Contains(got, "A      ") {
		t.Errorf("advice should show the A form:\n%s", got)
	}
	if !strings.Contains(got, "CNAME") {
		t.Errorf("advice should show the CNAME form:\n%s", got)
	}
	if !strings.Contains(got, "One record, not both") {
		t.Errorf("advice should say to pick one:\n%s", got)
	}
}

// Tunnel shape: a public https:// site with our TLS off — Tailscale Serve or a Cloudflare Tunnel
// terminating TLS in front, plain HTTP into the stack.
func TestTunnelFrontedCaddyfileTrustsTheProxy(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://box.tail1234.ts.net"
	cfg.tls = tlsOff
	cfg.httpPort = 8080

	got := caddyfileFor(cfg)
	// Without this line Caddy rewrites X-Forwarded-Proto to http, Keycloak's realm demands
	// https, and every sign-in behind a tunnel is refused while the browser shows a padlock.
	if !strings.Contains(got, "trusted_proxies static private_ranges") {
		t.Errorf("tunnel mode must trust the fronting proxy's headers:\n%s", got)
	}
	if !strings.Contains(got, "auto_https off") {
		t.Errorf("tunnel mode serves plain HTTP:\n%s", got)
	}
	// A plain --tls off WITHOUT a tunnel (http:// site) must not start trusting proxies.
	plain := defaultInstallConfig()
	plain.site = "http://192.168.1.50:8080"
	if strings.Contains(caddyfileFor(plain), "trusted_proxies") {
		t.Error("a plain http site is not tunnel-fronted and must not trust forwarded headers")
	}
}

// The report tells the operator exactly where to point the tunnel, and flags the plain-HTTP
// LAN bypass when bound to every interface.
func TestTunnelReportNamesTheTargetAndTheBindRisk(t *testing.T) {
	var out bytes.Buffer
	cfg := defaultInstallConfig()
	cfg.site = "https://pl.example.com"
	cfg.tls = tlsOff
	cfg.httpPort = 8080
	cfg.dir = "/opt/partyline"
	printInstallReport(cfg, installOps{out: &out})
	s := out.String()
	if !strings.Contains(s, "tailscale serve --bg --https=443 http://127.0.0.1:8080") {
		t.Errorf("report should print the tailscale command with the real port:\n%s", s)
	}
	if !strings.Contains(s, "cloudflared") {
		t.Errorf("report should cover the cloudflared shape:\n%s", s)
	}
	if !strings.Contains(s, "walk around the tunnel") {
		t.Errorf("0.0.0.0 + plain HTTP behind a tunnel is a LAN bypass and must be flagged:\n%s", s)
	}

	// Bound to loopback: the warning goes away.
	var quiet bytes.Buffer
	cfg.bind = "127.0.0.1"
	printInstallReport(cfg, installOps{out: &quiet})
	if strings.Contains(quiet.String(), "walk around") {
		t.Error("loopback bind needs no bypass warning")
	}
}

// Behind a tunnel the public port is 443 no matter where our HTTP port moved; the remapped-port
// warning would tell the operator to break every link.
func TestTunnelSuppressesTheSitePortWarning(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://pl.example.com"
	cfg.tls = tlsOff
	cfg.httpPort = 8080
	cfg.httpsPort = 8443
	if note := sitePortNote(cfg); note != "" {
		t.Errorf("tunnel mode must not ask for a port in the site: %q", note)
	}
}

// MagicDNS lives on the host; a container asking Docker's resolver for a .ts.net name gets
// nothing, and the web container must resolve the site to reach the identity provider.
func TestTsNetSiteHintsTheMagicDNSResolver(t *testing.T) {
	cfg := defaultInstallConfig()
	cfg.site = "https://box.tail1234.ts.net"
	for _, f := range installMenuFields() {
		if f.label != "dns" {
			continue
		}
		note := f.note(cfg, installOps{})
		if !strings.Contains(note, "100.100.100.100") {
			t.Errorf("a .ts.net site with no resolver should point at MagicDNS: %q", note)
		}
		return
	}
	t.Fatal("no dns field in the menu")
}

// `ptln server tunnel` answers from the box: real port, real tailnet name, real state — a recipe
// with placeholders is homework.
func TestServerTunnelReadsTheBox(t *testing.T) {
	dir := writeInstall(t, "SITE_URL=http://192.168.1.170:8880\nHTTP_PORT=8880\n")
	var out bytes.Buffer
	ops := installOps{
		out:      &out,
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		run: func(_ string, name string, args ...string) (string, error) {
			if name == "tailscale" && len(args) > 0 && args[0] == "status" {
				return `{"BackendState":"Running","Self":{"DNSName":"monolith.tail1234.ts.net."}}`, nil
			}
			return "", nil
		},
	}
	// Not tunnel-shaped: the http site means the whole address-change sequence is printed, with
	// the box's own tailnet name and port substituted in.
	if serverTunnel(dir, ops) {
		t.Fatal("an http-site install is not tunnel-fronted; the command reports work to do")
	}
	s := out.String()
	for _, want := range []string{
		"SITE_URL=https://monolith.tail1234.ts.net",
		"OIDC_ISSUER=https://monolith.tail1234.ts.net/auth/realms/partyline",
		"rm " + dir + "/Caddyfile",
		"--tls off --bind 127.0.0.1 --http-port 8880",
		"sessions end when the issuer changes",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}

// A tunnel-shaped install gets the serve command with the port filled in, and the logged-out
// tailscale state is named rather than a command that would fail.
func TestServerTunnelShapedInstall(t *testing.T) {
	dir := writeInstall(t, "SITE_URL=https://pl.tail1234.ts.net\nHTTP_PORT=8080\nBIND_ADDR=127.0.0.1\n")
	os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte("{\n\ttrusted_proxies static private_ranges\n}\n"), 0o644)
	var out bytes.Buffer
	ops := installOps{
		out:      &out,
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		run: func(_ string, name string, args ...string) (string, error) {
			return `{"BackendState":"NeedsLogin","Self":{"DNSName":""}}`, nil
		},
	}
	if !serverTunnel(dir, ops) {
		t.Fatalf("a shaped install should report ready:\n%s", out.String())
	}
	s := out.String()
	if !strings.Contains(s, "tailscale serve --bg --https=443 http://127.0.0.1:8080") {
		t.Errorf("serve command should carry the real port:\n%s", s)
	}
	if !strings.Contains(s, "logged out") {
		t.Errorf("a logged-out tailscale must be named before its command is offered:\n%s", s)
	}
	if strings.Contains(s, "0.0.0.0") {
		t.Errorf("loopback bind needs no bypass warning:\n%s", s)
	}
}

// The menu's tunnel preset sets TLS off and moves the bind to loopback — unless the operator
// already chose a bind, which is theirs.
func TestCertificateMenuTunnelPreset(t *testing.T) {
	var out bytes.Buffer
	ops := installOps{out: &out, in: bufio.NewReader(strings.NewReader("5\n"))}
	cfg := defaultInstallConfig()
	cfg.site = "https://box.tail1234.ts.net"

	got, changed, _ := editTLS(cfg, ops)
	if !changed || got.tls != tlsOff {
		t.Fatalf("preset should set tls off, got %v", got.tls)
	}
	if got.bind != "127.0.0.1" || !got.explicit["BIND_ADDR"] {
		t.Errorf("preset should bind loopback explicitly, got %q", got.bind)
	}

	// An operator-chosen bind survives.
	cfg2 := defaultInstallConfig()
	cfg2.site = "https://box.tail1234.ts.net"
	cfg2.bind = "10.0.0.4"
	cfg2.explicit["BIND_ADDR"] = true
	ops2 := installOps{out: &out, in: bufio.NewReader(strings.NewReader("tunnel\n"))}
	got2, _, _ := editTLS(cfg2, ops2)
	if got2.bind != "10.0.0.4" {
		t.Errorf("an explicit bind is the operator's, got %q", got2.bind)
	}
}

// Debian points the machine's own hostname at 127.0.1.1 in /etc/hosts. The loopback guard only
// matched three spellings, so "https://monolith" installed a box no container could sign in to.
func TestWholeLoopbackRangeIsRefused(t *testing.T) {
	for _, bad := range []string{"https://127.0.1.1", "http://127.5.5.5:8080"} {
		if err := rejectLoopbackSite(bad); err == nil {
			t.Errorf("%s is loopback and must be refused", bad)
		}
	}
	if err := rejectLoopbackSite("http://192.168.1.170:8880"); err != nil {
		t.Errorf("a LAN address is fine: %v", err)
	}
}

// A hostname resolving ONLY to loopback is a hosts-file alias — the fix is different from a stale
// public record, so it is its own state and the menu names an address that would work.
func TestHostsFileAliasIsItsOwnState(t *testing.T) {
	local := []string{"192.168.1.170"}
	r, addrs := checkSiteDNS("https://monolith",
		func(string) ([]string, error) { return []string{"127.0.1.1"}, nil }, local)
	if r != dnsResolvesLoopback {
		t.Fatalf("127.0.1.1-only should read as a hosts-file alias, got %v", r)
	}
	if !strings.Contains(dnsNote(r, addrs), "hosts-file alias") {
		t.Errorf("note should say what it is: %q", dnsNote(r, addrs))
	}

	var out bytes.Buffer
	ops := installOps{
		out:      &out,
		in:       bufio.NewReader(strings.NewReader("\nq\n")),
		portBusy: func(string, int) bool { return false },
		run:      func(string, string, ...string) (string, error) { return "", nil },
		lookup:   func(string) ([]string, error) { return []string{"127.0.1.1"}, nil },
		localIPs: func() []string { return local },
	}
	cfg := defaultInstallConfig()
	cfg.site = "https://monolith"
	if _, ok := runSetupMenu(cfg, ops); ok {
		t.Error("a hosts-file alias must not install")
	}
	if !strings.Contains(out.String(), "http://192.168.1.170") {
		t.Errorf("the blocker should name an address that works:\n%s", out.String())
	}
}

// Certificate "off" means plain HTTP: the site scheme follows, instead of leaving an https site
// with TLS off — which is the TUNNEL shape, and got LAN operators tailscale instructions.
func TestCertificateOffFlipsTheSiteToHTTP(t *testing.T) {
	var out bytes.Buffer
	cfg := defaultInstallConfig()
	cfg.site = "https://monolith" // what typing "monolith" produces
	got, changed, msg := editTLS(cfg, installOps{out: &out, in: bufio.NewReader(strings.NewReader("4\n"))})
	if !changed || got.site != "http://monolith" {
		t.Errorf("off should flip the scheme, got %q", got.site)
	}
	if !strings.Contains(msg, "http://monolith") {
		t.Errorf("the status should show the new site: %q", msg)
	}
	if tunnelFronted(got) {
		t.Error("off on a LAN site must not read as tunnel mode")
	}

	// And the tunnel preset goes the other way: its public side is https by definition.
	cfg2 := defaultInstallConfig()
	cfg2.site = "http://box.tail1234.ts.net"
	got2, _, _ := editTLS(cfg2, installOps{out: &out, in: bufio.NewReader(strings.NewReader("5\n"))})
	if got2.site != "https://box.tail1234.ts.net" || !tunnelFronted(got2) {
		t.Errorf("tunnel preset should make the site https, got %q", got2.site)
	}
}

// Storage is ON by default — the owner's call, reversing the condense default. A fresh install
// writes MINIO_REPLICAS=1 so env-bootstrap wires S3 at the bundled MinIO; --no-minio flips it and
// the bootstrap gate keeps S3 unwired.
func TestMinioDefaultsOn(t *testing.T) {
	if !defaultInstallConfig().minio {
		t.Fatal("storage defaults on")
	}
	dir := t.TempDir()
	cfg := defaultInstallConfig()
	cfg.dir = dir
	var out bytes.Buffer
	if err := writeInstallEnvOverrides(cfg, installOps{writeFile: os.WriteFile, out: &out}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(got), "MINIO_REPLICAS=1") {
		t.Errorf("fresh install should run the bundled MinIO:\n%s", string(got))
	}
}
