package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// server_install_report.go — the parts of `ptln server install` that talk to the operator: the
// prompts, the plan, and the two endings (done, or stopped part-way).

// installPrompt fills the gaps the flags left. Interactive by default because the one value nobody
// can guess is the public URL, and a wrong one bakes itself into .env and every issued certificate.
func installPrompt(cfg installConfig, ops installOps) (installConfig, error) {
	if cfg.site == "" && (cfg.assumeYes || ops.in == nil) {
		return cfg, fmt.Errorf("--site is required with --yes: pass the public URL this box will serve, e.g. --site https://partyline.example.com")
	}
	if cfg.site == "" {
		fmt.Fprintf(ops.out, "\nWhat URL will this box serve on?\n")
		fmt.Fprintf(ops.out, "  Include the scheme. This goes into .env and into every certificate.\n")
		fmt.Fprintf(ops.out, "  e.g. https://partyline.example.com\n\n  URL: ")
		line, err := ops.in.ReadString('\n')
		if err != nil {
			return cfg, fmt.Errorf("could not read the URL")
		}
		cfg.site = strings.TrimSpace(line)
	}
	site, err := normalizeSiteURL(cfg.site)
	if err != nil {
		return cfg, err
	}
	if err := rejectLoopbackSite(site); err != nil {
		return cfg, err
	}
	cfg.site = site
	return cfg, nil
}

// normalizeSiteURL accepts what an operator actually types and rejects what cannot work. A bare
// hostname is the common case and is NOT an error — it is completed to https://, which is what
// they meant. A trailing slash is trimmed because every consumer of SITE_URL joins paths onto it.
func normalizeSiteURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("the URL cannot be empty")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "", fmt.Errorf("the URL must be http:// or https:// — got %q", s)
	}
	// Split scheme from host BEFORE trimming slashes: "https://" trims down to "https:", which
	// then survives every prefix check and reads as a valid host.
	scheme, host, _ := strings.Cut(s, "://")
	host = strings.TrimRight(host, "/")
	// A bare hostname (`monolith`), a LAN address (`192.168.1.50`) and a `.local` are all
	// legitimate here — a self-hosted box frequently has no domain at all. Only reject what
	// cannot be an address.
	if host == "" || strings.ContainsAny(host, " \t") {
		return "", fmt.Errorf("that does not look like a URL: %q", s)
	}
	return scheme + "://" + host, nil
}

// rejectLoopbackSite refuses a --site that only this machine can reach.
//
// SITE_URL is not just what you type in a browser: the web CONTAINER uses it to reach Keycloak for
// OIDC discovery, and to fetch the JWKS it verifies every sign-in against. Inside a container,
// 127.0.0.1 is the container — so a loopback site installs cleanly, serves pages, passes the health
// check, and can never sign anybody in. The failure surfaces as ECONNREFUSED from a component the
// operator was not thinking about, long after the install said "Done".
//
// Refused at the door rather than warned about, because there is no configuration that rescues it.
func rejectLoopbackSite(site string) error {
	_, host, _ := strings.Cut(site, "://")
	host = stripHostPort(strings.TrimRight(host, "/"))
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return fmt.Errorf("--site %s only works from this machine, and not even from inside the containers:\n"+
			"  the web container reaches the identity provider over this address, and there 127.0.0.1 is\n"+
			"  the container itself. Sign-in would fail on a box that otherwise looks healthy.\n"+
			"  Use an address other machines can reach — a LAN IP, a Tailscale name, or a hostname:\n"+
			"    ptln server install --site http://192.168.1.50:8880 --tls off", site)
	}
	return nil
}

func printInstallPlan(cfg installConfig, steps []installStep, ops installOps) {
	fmt.Fprintf(ops.out, "\nPlan\n\n")
	fmt.Fprintf(ops.out, "  site           %s\n", cfg.site)
	fmt.Fprintf(ops.out, "  directory      %s\n", cfg.dir)
	fmt.Fprintf(ops.out, "  interface      %s\n", cfg.bind)
	if cfg.noCaddy {
		fmt.Fprintf(ops.out, "  edge           not running (--no-caddy: something else terminates TLS)\n")
	} else {
		fmt.Fprintf(ops.out, "  http / https   %d / %d\n", cfg.httpPort, cfg.httpsPort)
	}
	fmt.Fprintf(ops.out, "  relay          %d\n", cfg.relayPort)
	if !cfg.noCaddy {
		fmt.Fprintf(ops.out, "  certificate    %s\n", tlsModeNote(cfg))
	}
	if note := sitePortNote(cfg); note != "" {
		fmt.Fprint(ops.out, note)
	}

	fmt.Fprintf(ops.out, "\n  Steps:\n")
	for i, s := range steps {
		fmt.Fprintf(ops.out, "    %d. %s\n", i+1, s.what)
	}
	fmt.Fprintf(ops.out, "\n  Nothing outside %s is modified. Your .env is only ever added to.\n", cfg.dir)
}

// sitePortNote catches the footgun in a remapped-port install: SITE_URL is what the application
// builds its own links from, so if the box answers on :8443 and --site says no port, every link it
// generates is dead. The device-code sign-in makes this immediate — it hands you a
// verification_uri to open in a browser, and that URL simply does not resolve.
func sitePortNote(cfg installConfig) string {
	if cfg.noCaddy || siteHasPort(cfg.site) {
		return ""
	}
	// Behind a tunnel the public port IS 443 regardless of where our HTTP port moved — the
	// tunnel bridges them. Telling the operator to put :8080 in the site would break every
	// link the moment the tunnel is up.
	if tunnelFronted(cfg) {
		return ""
	}
	if cfg.httpsPort == 443 && cfg.httpPort == 80 {
		return ""
	}
	return fmt.Sprintf("\n  This box answers on :%d, but --site names no port — so links the app\n"+
		"  builds for itself (the sign-in URL among them) will point at the default\n"+
		"  and fail. Include the port:  --site %s:%d\n",
		cfg.httpsPort, strings.TrimRight(cfg.site, "/"), cfg.httpsPort)
}

func confirmInstall(ops installOps) bool {
	if ops.in == nil {
		return false
	}
	fmt.Fprintf(ops.out, "\nProceed? [y/N] ")
	line, err := ops.in.ReadString('\n')
	if err != nil {
		return false
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

func printInstallReport(cfg installConfig, ops installOps) {
	fmt.Fprintf(ops.out, "\nDone. partyline is running at %s\n\n", cfg.site)
	if tunnelFronted(cfg) {
		fmt.Fprintf(ops.out, "  The stack serves plain HTTP on port %d for YOUR tunnel to front. Point it here:\n\n", cfg.httpPort)
		fmt.Fprintf(ops.out, "    Tailscale:   tailscale serve --bg --https=443 http://127.0.0.1:%d\n", cfg.httpPort)
		fmt.Fprintf(ops.out, "    Cloudflare:  cloudflared tunnel ... --url http://127.0.0.1:%d\n", cfg.httpPort)
		fmt.Fprintf(ops.out, "                 (or in config.yml:  service: http://localhost:%d)\n\n", cfg.httpPort)
		if cfg.bind == "0.0.0.0" {
			fmt.Fprintf(ops.out, "  ⚠ bound to 0.0.0.0: anyone on this network can reach the plain-HTTP port and\n")
			fmt.Fprintf(ops.out, "    walk around the tunnel. Re-run with --bind 127.0.0.1 unless that is intended.\n\n")
		}
	}
	fmt.Fprintf(ops.out, "  Next: open %s and finish setup in the browser — the first account\n", cfg.site)
	fmt.Fprintf(ops.out, "  to sign in becomes the instance admin.\n\n")
	fmt.Fprintf(ops.out, "  Point a CLI at this box:  ptln login %s\n", cfg.site)
	fmt.Fprintf(ops.out, "  Check it over:            ptln server doctor\n")
	fmt.Fprintf(ops.out, "  Logs:                     cd %s && docker compose logs -f\n", cfg.dir)
	fmt.Fprintf(ops.out, "  Stop:                     cd %s && docker compose down\n\n", cfg.dir)
	fmt.Fprintf(ops.out, "  Secrets were generated into %s — back that file up.\n", filepath.Join(cfg.dir, ".env"))
	fmt.Fprintf(ops.out, "  It is the only copy, and the database cannot be read without it.\n")
}

// reportPartial is what an operator sees when a step fails. It names exactly what was done, so the
// thing they debug is their machine and not a guess about our state — the specific failure the
// bootstrap-is-read-only decision was written to avoid.
func reportPartial(cfg installConfig, done []installStep, ops installOps) {
	fmt.Fprintf(ops.out, "\nStopped part-way. This is what had already been done:\n\n")
	if len(done) == 0 {
		fmt.Fprintf(ops.out, "  nothing — the first step failed, so the machine is unchanged.\n")
	}
	for i, s := range done {
		fmt.Fprintf(ops.out, "  %d. %s\n", i+1, s.what)
	}
	fmt.Fprintf(ops.out, "\n  Everything above is under %s and is safe to leave in place:\n", cfg.dir)
	fmt.Fprintf(ops.out, "  re-running this command reconciles rather than starting over, and\n")
	fmt.Fprintf(ops.out, "  never rewrites a .env that already exists.\n\n")
	fmt.Fprintf(ops.out, "  Inspect:  cd %s && docker compose ps && docker compose logs --tail=50\n", cfg.dir)
	fmt.Fprintf(ops.out, "  Undo all: cd %s && docker compose down -v   (-v also deletes the database)\n", cfg.dir)
}

// unixWritable reports whether this process can create files in dir. It tries the real thing rather
// than reasoning about mode bits and ownership, which get root, sudo, group membership and ACLs
// wrong in different ways on different boxes.
func unixWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".ptln-write-probe-")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

var _ = syscall.Stat_t{}
