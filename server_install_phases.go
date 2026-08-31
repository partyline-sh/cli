package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// server_install_phases.go — the five phases of `ptln server install`. See server_install.go for
// why the command exists and what each phase is allowed to do.

// installStep is one unit of work in the plan: what it will do, and — once applied — the assertion
// that it actually happened. A step without a check cannot be part of this installer; "ran without
// error" is not evidence, because the failure this design exists to prevent is the one where every
// command exits 0 and the box still does not work.
type installStep struct {
	what  string
	do    func(installConfig, installOps) error
	check func(installConfig, installOps) error
}

func runInstall(cfg installConfig, ops installOps) bool {
	fmt.Fprintf(ops.out, "partyline self-host installer\n\n")

	// ── the setup menu ──────────────────────────────────────────────────────────────────────
	// EVERY choice, before anything is checked. It runs first on purpose: a setting you are
	// about to change must not be reported as a problem you have to go and fix.
	//
	// The previous attempt at this sat AFTER the preflight report, so preflight returned before
	// it and the menu was dead code — an operator got "pass --http-port (8080 is free)" and a
	// refusal, which is the same wall with a better sign on it.
	// A directory that already holds an install seeds the menu with its CURRENT settings, so
	// re-running the installer is how you change them — a wrong site address was uncorrectable
	// without this (the .env was only ever added to, the Caddyfile never rewritten).
	cfg = prefillFromExisting(cfg, ops)
	cfg, ok := runSetupMenu(cfg, ops)
	if !ok {
		fmt.Fprintf(ops.out, "\nCancelled. Nothing was written.\n")
		return false
	}
	cfg = ensureSitePort(cfg, ops.out)

	// A --yes run never saw the menu, so the site can still be missing here.
	var err error
	if cfg, err = installPrompt(cfg, ops); err != nil {
		fmt.Fprintf(ops.out, "\n%v\nNothing was written.\n", err)
		return false
	}

	// ── phase 1: preflight ──────────────────────────────────────────────────────────────────
	// Reads the machine. Writes nothing, including on the happy path. After the menu, this is
	// about the things a person cannot answer away: no docker, no disk, no compose plugin.
	problems := installPreflight(cfg, ops)
	for _, p := range problems {
		fmt.Fprintf(ops.out, "  %s\n", p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(ops.out, "\nFix those and run again. Nothing was written.\n")
		return false
	}

	// ── phase 2: plan ───────────────────────────────────────────────────────────────────────
	steps := installSteps(cfg)
	printInstallPlan(cfg, steps, ops)
	if cfg.dryRun {
		fmt.Fprintf(ops.out, "\n--dry-run: stopping here. Nothing was written.\n")
		return true
	}
	if !cfg.assumeYes && !confirmInstall(ops) {
		fmt.Fprintf(ops.out, "\nCancelled. Nothing was written.\n")
		return false
	}

	// ── phase 3: apply ──────────────────────────────────────────────────────────────────────
	fmt.Fprintf(ops.out, "\nApplying.\n")
	for i, s := range steps {
		fmt.Fprintf(ops.out, "  [%d/%d] %s ... ", i+1, len(steps), s.what)
		if err := s.do(cfg, ops); err != nil {
			fmt.Fprintf(ops.out, "FAILED\n\n%v\n", err)
			reportPartial(cfg, steps[:i], ops)
			return false
		}
		// Assert the EFFECT, not the exit code.
		if err := s.check(cfg, ops); err != nil {
			fmt.Fprintf(ops.out, "ran, but did not take effect\n\n%v\n", err)
			reportPartial(cfg, steps[:i], ops)
			return false
		}
		fmt.Fprintf(ops.out, "ok\n")
	}

	// ── phase 4: verify ─────────────────────────────────────────────────────────────────────
	fmt.Fprintf(ops.out, "\nVerifying.\n")
	recordInstallDir(cfg.dir)
	if failures := installVerify(cfg, ops); len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintf(ops.out, "  %s\n", f)
		}
		fmt.Fprintf(ops.out, "\nThe stack is installed and running, but it is not serving correctly.\n")
		fmt.Fprintf(ops.out, "Nothing needs undoing — fix the above and re-run this command; it reconciles.\n")
		fmt.Fprintf(ops.out, "Logs: cd %s && docker compose logs --tail=50\n", cfg.dir)
		return false
	}

	// ── phase 5: report ─────────────────────────────────────────────────────────────────────
	printInstallReport(cfg, ops)
	return true
}

// installPreflight asks every question that can stop an install, and returns one line per problem.
func installPreflight(cfg installConfig, ops installOps) []string {
	var problems []string
	fmt.Fprintf(ops.out, "Checking this machine.\n")

	if _, err := ops.lookPath("docker"); err != nil {
		problems = append(problems, "docker is not on PATH — install Docker Engine 20.10+ (https://docs.docker.com/engine/install/)")
	} else if out, err := ops.run("", "docker", "compose", "version"); err != nil {
		problems = append(problems, "`docker compose` is not available — install the Compose v2 plugin (you have docker, but not compose): "+installFirstLine(out))
	}

	// The parent must exist and be writable. Checking the parent rather than the target is what
	// makes a first install (no target yet) and a re-run (target exists) take the same path.
	parent := filepath.Dir(strings.TrimRight(cfg.dir, "/"))
	if st, err := ops.stat(parent); err != nil {
		problems = append(problems, fmt.Sprintf("%s does not exist — create it, or pick another --dir", parent))
	} else if !st.IsDir() {
		problems = append(problems, fmt.Sprintf("%s is not a directory — pick another --dir", parent))
	} else if err := unixWritable(parent); err != nil {
		// Name a command that RESOLVES. `sudo ptln` fails when ptln is in ~/.local/bin, which
		// is where install.sh used to land it — an operator followed this advice verbatim and
		// got "sudo: ptln: command not found".
		problems = append(problems, fmt.Sprintf("%s is not writable by this user.\n"+
			"      Install somewhere you own:  --dir $HOME/partyline\n"+
			"      Or run it as root:          %s",
			parent, sudoHint("server install")))
	}

	for _, p := range installWantedPorts(cfg) {
		// Busy is only a problem if it is not already US: a re-run on a live box finds its own
		// stack holding the port, and calling a working install broken sends the operator to
		// stop the very thing they are trying to install.
		if ops.portBusy(cfg.bind, p.port) && !installAlreadyOurs(cfg, ops) {
			// Reached only when nobody could be asked — `--yes`, or no terminal. Interactive
			// runs resolve this by prompting, so this text is for a script.
			hint := ""
			if free := firstFreePort(cfg.bind, p.flag, ops.portBusy); free > 0 {
				hint = fmt.Sprintf(" (%d is free)", free)
			}
			problems = append(problems, fmt.Sprintf("%s port %d is already in use on %s — pass --%s-port%s",
				p.label, p.port, cfg.bind, p.flag, hint))
		}
	}
	return problems
}

type wantedPort struct {
	label, flag string
	port        int
}

func installWantedPorts(cfg installConfig) []wantedPort {
	ports := []wantedPort{{"relay", "relay", cfg.relayPort}}
	if !cfg.noCaddy {
		ports = append(ports,
			wantedPort{"HTTP", "http", cfg.httpPort},
			wantedPort{"HTTPS", "https", cfg.httpsPort})
	}
	return ports
}

// installAlreadyOurs reports whether this directory is already a running partyline stack, which is
// what makes a re-run a reconcile instead of a collision.
func installAlreadyOurs(cfg installConfig, ops installOps) bool {
	if _, err := ops.stat(filepath.Join(cfg.dir, "docker-compose.yml")); err != nil {
		return false
	}
	out, err := ops.run(cfg.dir, "docker", "compose", "ps", "--quiet")
	return err == nil && strings.TrimSpace(out) != ""
}

// installSteps is the ordered plan. Every entry does one thing and proves it.
func installSteps(cfg installConfig) []installStep {
	return []installStep{
		{
			what: "create " + cfg.dir,
			do: func(c installConfig, o installOps) error {
				return o.mkdirAll(c.dir, 0o755)
			},
			check: func(c installConfig, o installOps) error {
				st, err := o.stat(c.dir)
				if err != nil {
					return fmt.Errorf("%s was not created: %w", c.dir, err)
				}
				if !st.IsDir() {
					return fmt.Errorf("%s exists but is not a directory", c.dir)
				}
				return nil
			},
		},
		{
			what: "write the stack files",
			do: func(c installConfig, o installOps) error {
				_, err := writeStack(c.dir)
				return err
			},
			check: func(c installConfig, o installOps) error {
				for _, f := range stackFiles {
					if _, err := o.stat(filepath.Join(c.dir, f)); err != nil {
						return fmt.Errorf("%s is missing after the write: %w", f, err)
					}
				}
				return nil
			},
		},
		{
			what: "write the Caddyfile for " + cfg.site,
			do: func(c installConfig, o installOps) error {
				return reconcileCaddyfile(c, o)
			},
			check: func(c installConfig, o installOps) error {
				_, err := o.stat(filepath.Join(c.dir, "Caddyfile"))
				return err
			},
		},
		{
			what: "fill in .env (secrets are generated here, never printed)",
			do: func(c installConfig, o installOps) error {
				// ORDER MATTERS. The operator's port/interface choices go in FIRST, because
				// env-bootstrap.sh writes its own defaults for exactly these names and only
				// ever adds what is missing — so running it first meant it wrote HTTP_PORT=80
				// and the --http-port the plan had just promised was silently dropped. That
				// really happened, on a box where the plan said 8880 and Caddy bound 80.
				if err := writeInstallEnvOverrides(c, o); err != nil {
					return err
				}
				// env-bootstrap.sh is idempotent and only ever ADDS, so a re-run cannot
				// rotate a live box's secrets out from under its own database.
				out, err := o.run(c.dir, "./env-bootstrap.sh", c.site)
				if err != nil {
					return fmt.Errorf("env-bootstrap.sh failed: %s", installFirstLine(out))
				}
				return nil
			},
			check: func(c installConfig, o installOps) error {
				if _, err := o.stat(filepath.Join(c.dir, ".env")); err != nil {
					return fmt.Errorf(".env was not created: %w", err)
				}
				// Names only — a value from .env must never reach this output.
				missing := missingEnvNames(filepath.Join(c.dir, ".env"))
				if len(missing) > 0 {
					return fmt.Errorf(".env is still missing %d required variable(s): %s",
						len(missing), strings.Join(missing, ", "))
				}
				return nil
			},
		},
		{
			what: "write the database schema",
			do: func(c installConfig, o installOps) error {
				_, err := writeMigrations(c.dir)
				return err
			},
			check: func(c installConfig, o installOps) error {
				// apply-migrations.sh refuses to run against an empty migrations directory,
				// so assert the directory is populated here rather than discovering it two
				// steps later with the images already pulled.
				if _, err := o.stat(filepath.Join(c.dir, "migrations", "BASELINE")); err != nil {
					return fmt.Errorf("migrations/BASELINE is missing after the write: %w", err)
				}
				return nil
			},
		},
		{
			what: "point the containers at your resolver",
			do: func(c installConfig, o installOps) error {
				path := filepath.Join(c.dir, "docker-compose.override.yml")
				if strings.TrimSpace(c.dns) == "" {
					// No resolver chosen. Remove a file from a previous run rather than
					// leaving one that silently keeps overriding.
					_ = os.Remove(path)
					return nil
				}
				return o.writeFile(path, []byte(dnsOverrideYAML(c.dns)), 0o644)
			},
			check: func(c installConfig, o installOps) error {
				path := filepath.Join(c.dir, "docker-compose.override.yml")
				_, err := o.stat(path)
				if strings.TrimSpace(c.dns) == "" {
					if err == nil {
						return fmt.Errorf("%s still exists but no resolver is configured", path)
					}
					return nil
				}
				return err
			},
		},
		{
			what: "pull the images",
			do: func(c installConfig, o installOps) error {
				out, err := o.run(c.dir, "docker", "compose", "pull", "--quiet")
				if err != nil {
					return fmt.Errorf("docker compose pull failed: %s%s", installFirstLine(out), pullHint(out))
				}
				return nil
			},
			check: func(c installConfig, o installOps) error { return nil },
		},
		{
			what: "start the stack",
			do: func(c installConfig, o installOps) error {
				// --remove-orphans: the stack no longer defines a ticker service, and an
				// upgrade must retire the old container rather than leave it POSTing the
				// tick alongside the web's own at double rate.
				out, err := o.run(c.dir, "docker", "compose", "up", "-d", "--remove-orphans")
				if err != nil {
					return fmt.Errorf("docker compose up failed: %s", installFirstLine(out))
				}
				// `up -d` does NOT restart a container whose service definition is unchanged,
				// and the Caddyfile is a mounted FILE — so on a re-install that rewrote it,
				// Caddy keeps serving the old config. Observed: a corrected Caddyfile on disk,
				// a container 10 minutes old still listening on the wrong ports, and every
				// HTTPS request timing out. Reloading is cheap and idempotent.
				if !c.noCaddy {
					if _, err := o.run(c.dir, "docker", "compose", "restart", "caddy"); err != nil {
						// Not fatal: the stack is up, and verify will say if the edge is wrong.
						fmt.Fprintf(o.out, "(could not reload the edge; verify will check it) ")
					}
				}
				return nil
			},
			check: func(c installConfig, o installOps) error {
				out, err := o.run(c.dir, "docker", "compose", "ps", "--quiet")
				if err != nil || strings.TrimSpace(out) == "" {
					return fmt.Errorf("no containers are running after `up -d`")
				}
				return nil
			},
		},
		{
			what: "point the sign-in client at the site",
			// The client lives in Keycloak's database; the realm import only ever ran once.
			// Idempotent: sets redirect URI + web origin to the current site on every run,
			// which is what makes a site change (reconfigure) actually sign people in.
			do: func(c installConfig, o installOps) error {
				return kcadmSyncSignIn(c.dir, c.site, o)
			},
			check: func(c installConfig, o installOps) error {
				return nil // the do already read the client back and asserted the URI
			},
		},
		{
			what: "apply database migrations",
			do: func(c installConfig, o installOps) error {
				out, err := o.run(c.dir, "./apply-migrations.sh")
				if err != nil {
					return fmt.Errorf("apply-migrations.sh failed: %s", installFirstLine(out))
				}
				return nil
			},
			check: func(c installConfig, o installOps) error { return nil },
		},
	}
}

// writeInstallEnvOverrides appends the operator's port/interface choices to .env, if they are not
// already there. env-bootstrap.sh does not know about them; it only writes what it can derive.
func writeInstallEnvOverrides(c installConfig, o installOps) error {
	if o.out == nil {
		o.out = io.Discard // tests construct partial ops; the notes here are best-effort
	}
	if o.writeFile == nil {
		o.writeFile = os.WriteFile
	}
	path := filepath.Join(c.dir, ".env")
	text := ""
	if body, err := os.ReadFile(path); err == nil {
		text = string(body)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read .env: %w", err)
	}

	// A changed site address must reach every variable env-bootstrap DERIVED from the old one —
	// it only ever adds, so on a reconfigure the stale derivations would win and sign-in breaks
	// (the OIDC issuer among them). The site URL is plan-visible, never a secret, so naming the
	// change is fine; the values are rewritten by prefix so a hand-tuned suffix survives.
	if old := envValue(text, "SITE_URL"); old != "" && old != c.site {
		for _, k := range []string{"SITE_URL", "RELAY_API", "OIDC_PUBLIC_URL", "OIDC_ISSUER"} {
			if v := envValue(text, k); v != "" && strings.HasPrefix(v, old) {
				if replaced, ok := replaceEnvValue(text, k, c.site+strings.TrimPrefix(v, old)); ok {
					text = replaced
				}
			}
		}
		fmt.Fprintf(o.out, "(site changed: %s → %s) ", old, c.site)
	}

	want := []struct{ key, val string }{
		{"BIND_ADDR", c.bind},
		{"HTTP_PORT", strconv.Itoa(c.httpPort)},
		{"HTTPS_PORT", strconv.Itoa(c.httpsPort)},
		{"RELAY_PORT", strconv.Itoa(c.relayPort)},
	}
	// MINIO_REPLICAS: written on a FRESH install (so env-bootstrap sees the choice and skips the
	// S3 wiring when storage is off), and on an explicit flip. Never appended to an existing .env
	// that lacks it — absence there means compose's default of 1, i.e. a box already running
	// MinIO, and a re-run of the installer must not silently turn someone's storage off.
	if text == "" || c.explicit["MINIO_REPLICAS"] {
		v := "0"
		if c.minio {
			v = "1"
		}
		want = append(want, struct{ key, val string }{"MINIO_REPLICAS", v})
	}
	// Internal-CA mode: server-side OIDC (discovery, token, JWKS) fetches the PUBLIC https
	// URL from inside the web container, and Node does not trust Caddy's own CA — sign-in
	// dies with "OIDC discovery failed ... fetch failed" while the browser side looks fine.
	// Compose mounts Caddy's data volume read-only at /caddy-pki; this points Node at the
	// root certificate there. Written once; never removed (harmless when the mode changes —
	// Node ignores a missing file with a warning).
	if resolveTLSMode(c.tls, c.site) == tlsInternal {
		want = append(want, struct{ key, val string }{"NODE_EXTRA_CA_CERTS", caddyCAPath})
	}
	if c.noCaddy {
		want = append(want, struct{ key, val string }{"CADDY_REPLICAS", "0"})
	} else {
		// PostgREST over the container network, not the public URL.
		//
		// env-bootstrap.sh derives PGRST_URL from SITE_URL, which on a self-hosted box points
		// the web container at ITSELF — every server-side query dies with "fetch failed", the
		// device-code sign-in included. Only correct when there is an edge of ours to route
		// through, so it is skipped entirely under --no-caddy.
		want = append(want, struct{ key, val string }{"PGRST_URL", internalPgrstURL})
	}

	// Repair the specific bad value we shipped, and only that one. If PGRST_URL is currently the
	// site URL, it is env-bootstrap.sh's derivation rather than a choice anyone made, and it
	// cannot work from inside a container. Any OTHER value is the operator's and is left alone.
	if !c.noCaddy && envValueIs(text, "PGRST_URL", c.site) {
		if replaced, ok := replaceEnvValue(text, "PGRST_URL", internalPgrstURL); ok {
			text = replaced
			fmt.Fprintf(o.out, "(repaired PGRST_URL, which pointed the app at itself) ")
		}
	}

	var add []string
	for _, w := range want {
		switch {
		case c.explicit[w.key]:
			// The operator asked for this ON THIS RUN. An explicit flag beats whatever is
			// already in .env — otherwise `--http-port 8880` against an existing install is
			// accepted, printed in the plan, and then quietly ignored.
			if replaced, ok := replaceEnvValue(text, w.key, w.val); ok {
				text = replaced
				continue
			}
			add = append(add, w.key+"="+w.val)
		case !envHasName(text, w.key):
			add = append(add, w.key+"="+w.val)
		}
	}
	if len(add) > 0 {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n# Written by `ptln server install`.\n" + strings.Join(add, "\n") + "\n"
	}
	// 0600: this file holds every secret the box has.
	return o.writeFile(path, []byte(text), 0o600)
}

// replaceEnvValue rewrites an existing NAME= line in place, reporting whether it found one.
func replaceEnvValue(text, name, val string) (string, bool) {
	lines := strings.Split(text, "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), name+"=") {
			lines[i] = name + "=" + val
			found = true
		}
	}
	if !found {
		return text, false
	}
	return strings.Join(lines, "\n"), true
}

// envValue returns NAME's value from .env text ("" when unset). INTERNAL ONLY — a value from
// .env must never reach any output; callers use it to compute replacements, not to print.
func envValue(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// envValueIs reports whether NAME is set to exactly this value.
func envValueIs(text, name, val string) bool {
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+"="); ok {
			return strings.TrimSpace(v) == val
		}
	}
	return false
}

func envHasName(text, name string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), name+"=") {
			return true
		}
	}
	return false
}

// missingEnvNames returns the required variables this .env still does not set. NAMES ONLY — this
// function reads a file full of secrets and must never return a value out of it.
func missingEnvNames(path string) []string {
	set := map[string]bool{}
	for _, n := range envFileNames(path) {
		set[n] = true
	}
	var missing []string
	for _, v := range requiredEnv() {
		if !set[v] {
			missing = append(missing, v)
		}
	}
	return missing
}

// installVerify checks the RESULT. A box can pass every step and still not serve a page.
//
// It distinguishes two failures an operator must not have conflated, because the fix is completely
// different: the application not serving, and the CERTIFICATE not being issuable. Caddy gets its
// certificate from Let's Encrypt, which dials the site's public name — so on a box behind a private
// network, a Tailscale name, or a DNS record that does not exist yet, TLS fails while the stack
// underneath is perfectly healthy. Reporting that as "not serving" sends the operator to debug an
// application that is fine.
func installVerify(cfg installConfig, ops installOps) []string {
	var failures []string

	// Containers first: a crash-looping service is the likeliest reason for everything below.
	if out, err := ops.run(cfg.dir, "docker", "compose", "ps", "--format", "{{.Name}} {{.State}}"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.Contains(line, "restarting") || strings.Contains(line, "exited") {
				failures = append(failures, "container not healthy: "+line)
			}
		}
	}

	// Then the thing the operator actually wanted: a served page. Poll rather than sleep — the
	// first request after `up -d` routinely lands before the web container is listening, and a
	// fixed sleep is either too short (a false failure) or wasted time.
	app, tlsNote := verifyServing(cfg, ops)
	if app != "" {
		failures = append(failures, app)
	}
	if tlsNote != "" {
		// Not a failure. The stack serves; the certificate is a separate, later problem.
		fmt.Fprintf(ops.out, "  %s\n", tlsNote)
	}
	return failures
}

// verifyServing returns (appFailure, tlsNote). An empty appFailure means the stack is serving.
func verifyServing(cfg cfgAlias, ops installOps) (string, string) {
	// The plain-HTTP port first. Caddy answers it even when it has no certificate, so this is
	// the probe that isolates "is the application up" from "is TLS working".
	plain := installPlainBase(cfg)
	code, err := pollHTTP(ops, plain+"/api/health", 120*time.Second)
	if err != nil {
		return fmt.Sprintf("%s/api/health did not respond: %v", plain, err), ""
	}

	switch {
	case code == http.StatusOK:
		// Served directly (--no-caddy, or an edge not redirecting). Nothing more to check.
		return "", ""
	case code >= 300 && code < 400:
		// The edge is up and redirecting to the public https:// name, which is correct. Now
		// find out whether TLS actually works.
		secure := installLocalBase(cfg)
		sCode, sErr := pollHTTP(ops, secure+"/api/health", 60*time.Second)
		if sErr == nil && sCode == http.StatusOK {
			return "", ""
		}
		if sErr != nil && isTLSFailure(sErr) {
			return "", tlsNotIssuedNote(cfg)
		}
		if sErr != nil {
			return fmt.Sprintf("%s/api/health did not respond: %v", secure, sErr), ""
		}
		return fmt.Sprintf("%s/api/health answered %d, want 200", secure, sCode), ""
	default:
		return fmt.Sprintf("%s/api/health answered %d, want 200", plain, code), ""
	}
}

// cfgAlias exists only so the signature above reads at the call site; it is installConfig.
type cfgAlias = installConfig

func isTLSFailure(err error) bool {
	var re *tls.RecordHeaderError
	if errors.As(err, &re) {
		return true
	}
	var ce *tls.CertificateVerificationError
	if errors.As(err, &ce) {
		return true
	}
	// Caddy answers the handshake with an alert when it has no certificate for the name, which
	// surfaces as a plain error string rather than a typed one.
	msg := err.Error()
	return strings.Contains(msg, "tls:") || strings.Contains(msg, "certificate")
}

func tlsNotIssuedNote(cfg installConfig) string {
	return fmt.Sprintf("the stack is serving, but HTTPS on :%d has no certificate yet.\n"+
		"    Caddy gets one from Let's Encrypt, which dials %s on the PUBLIC port 80 or 443.\n"+
		"    That works once the name resolves publicly and those ports reach this box.\n"+
		"    On a private network or an internal name it never will — put `tls internal` in\n"+
		"    %s/Caddyfile, or terminate TLS in front of this stack with --no-caddy.",
		cfg.httpsPort, cfg.site, cfg.dir)
}

// installPlainBase is the non-TLS way in: the edge's HTTP port, or the web container directly when
// there is no edge.
func installPlainBase(cfg installConfig) string {
	host := cfg.bind
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if cfg.noCaddy {
		return fmt.Sprintf("http://%s:3000", host)
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.httpPort)
}

// installLocalBase is how to reach this box FROM this box, over TLS.
//
// Not cfg.site: the public URL may not resolve here at all (split-horizon DNS, a proxy in front, or
// simply no DNS record yet), and a verify that fails on the operator's DNS instead of on their
// install is a false alarm.
//
// HTTPS, not HTTP: Caddy answers :80 with a redirect to the public https:// name, so a plain HTTP
// probe measures the redirect and not the application. Going straight at the TLS port is what
// actually exercises the stack.
func installLocalBase(cfg installConfig) string {
	host := cfg.bind
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if cfg.noCaddy {
		// No edge: the web container publishes directly.
		return fmt.Sprintf("http://%s:3000", host)
	}
	return fmt.Sprintf("https://%s:%d", host, cfg.httpsPort)
}

func pollHTTP(ops installOps, url string, budget time.Duration) (int, error) {
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		code, err := ops.httpOK(url)
		if err == nil && code > 0 {
			return code, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out after %s", budget)
	}
	return 0, lastErr
}

// liveHTTPStatus dials the box being installed, from that same box.
//
// It does NOT follow redirects: Caddy answers with a redirect to the site's public name, and
// following it turns a local probe into a DNS-and-internet test that fails for reasons that have
// nothing to do with the install (this is exactly what happened on the first real box — the probe
// followed :8880 to https://<name>/ and reported "connection refused" on :443).
//
// TLS verification is skipped, and only here. The certificate is issued for the box's PUBLIC name
// while this request is to 127.0.0.1, so it cannot match by construction; and the question being
// asked is "is my own stack serving", not "do I trust this host". Nothing is sent — no credential,
// no body — and the answer used is only the status code.
func liveHTTPStatus(url string) (int, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // see above
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// pullHint explains the one pull failure whose message is actively misleading.
//
// The public registry is DOCKER HUB, not GHCR. GHCR receives every image, but the org disallows
// public packages, so those copies are internal: an anonymous manifest request returns 403 and
// docker reports it as "denied". This file used to claim the opposite — "our images are public,
// so this must be a stale login" — which sent anyone hitting it to debug their own credentials
// for a package they were never able to read. The stack's default is Hub now; this hint is for a
// box still pinned at ghcr.io, by an old .env, an old compose file, or WEB_IMAGE.
func pullHint(out string) string {
	low := strings.ToLower(out)
	if !strings.Contains(low, "denied") && !strings.Contains(low, "unauthorized") {
		return ""
	}
	hint := "\n\n      The public images are on DOCKER HUB. ghcr.io copies are internal, and an" +
		"\n      anonymous pull from there is refused — which docker reports as \"denied\"."
	if strings.Contains(low, "ghcr.io") {
		hint += "\n      This box is pulling from ghcr.io. Point it at Hub:\n\n" +
			"        WEB_IMAGE=docker.io/partyline/partyline-web\n" +
			"        RELAY_IMAGE=docker.io/partyline/partyline-relay\n"
	}
	hint += "\n      If you DO have access and it still fails, an expired credential is being sent" +
		"\n      instead of pulling anonymously:\n\n        docker logout ghcr.io\n"
	return hint
}

func installFirstLine(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "\n")
	if s == "" {
		return "(no output)"
	}
	lines := strings.Split(s, "\n")
	// Keep the last few, so a multi-line stack trace or a compose error with context survives.
	const keep = 8
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	return strings.Join(lines, "\n      ")
}
