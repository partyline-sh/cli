package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		in:       bufio.NewReader(strings.NewReader(stdin)),
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
