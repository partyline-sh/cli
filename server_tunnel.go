package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// server_tunnel.go — `ptln server tunnel`: everything needed to put a tunnel in front of THIS
// install, read from the box rather than recited from documentation.
//
// The docs carry the general recipe, but a recipe with placeholders is homework: which port, which
// name, is tailscale even logged in here, is the install already tunnel-shaped. This command
// answers from the machine — it reads the install's .env, asks tailscale for its own state, checks
// for cloudflared — and prints instructions with every value filled in. When the install is ready
// and tailscale is up, it offers to run the serve command itself.
//
// It never modifies the install. Reshaping an existing instance for a tunnel changes its ADDRESS,
// and an address change reaches into .env, the Caddyfile and Keycloak's issuer — that is the
// installer's reconcile, and this command prints the exact steps for it instead of half-doing them.

// tunnelFacts is what the box knows, gathered once.
type tunnelFacts struct {
	site      string // SITE_URL from .env
	httpPort  string
	bind      string
	tsName    string // this machine's tailnet DNS name, "" when tailscale is absent or logged out
	tsState   string // "", "ok", "needs-login", "not-installed"
	cfPresent bool
	shaped    bool // the install is already tunnel-fronted (https site, tunnel-trusting Caddyfile)
}

func gatherTunnelFacts(dir string, ops installOps) tunnelFacts {
	env := filepath.Join(dir, ".env")
	f := tunnelFacts{
		site:     readEnvValue(env, "SITE_URL"),
		httpPort: readEnvValue(env, "HTTP_PORT"),
		bind:     readEnvValue(env, "BIND_ADDR"),
	}
	if f.httpPort == "" {
		f.httpPort = "80"
	}
	if f.bind == "" {
		f.bind = "0.0.0.0"
	}

	f.tsState = "not-installed"
	if _, err := ops.lookPath("tailscale"); err == nil {
		f.tsState = "needs-login"
		if out, err := ops.run("", "tailscale", "status", "--json"); err == nil {
			var st struct {
				BackendState string
				Self         struct{ DNSName string }
			}
			if json.Unmarshal([]byte(out), &st) == nil && st.BackendState == "Running" {
				f.tsState = "ok"
				f.tsName = strings.TrimSuffix(st.Self.DNSName, ".")
			}
		}
	}
	if _, err := ops.lookPath("cloudflared"); err == nil {
		f.cfPresent = true
	}

	// Tunnel-shaped: an https site whose generated Caddyfile trusts the fronting proxy. The TLS
	// mode is not stored anywhere; the Caddyfile is the artifact that carries the decision.
	if body, err := os.ReadFile(filepath.Join(dir, "Caddyfile")); err == nil {
		f.shaped = strings.HasPrefix(f.site, "https://") && strings.Contains(string(body), "trusted_proxies")
	}
	return f
}

// serverTunnel prints the way forward, with the box's own values filled in.
func serverTunnel(dir string, ops installOps) bool {
	f := gatherTunnelFacts(dir, ops)
	out := ops.out

	fmt.Fprintf(out, "install: %s\n", dir)
	fmt.Fprintf(out, "  site      %s\n", f.site)
	fmt.Fprintf(out, "  http port %s   bind %s\n", f.httpPort, f.bind)
	switch f.tsState {
	case "ok":
		fmt.Fprintf(out, "  tailscale up — this machine is %s\n", f.tsName)
	case "needs-login":
		fmt.Fprintf(out, "  tailscale installed but logged out — `sudo tailscale up` first\n")
	default:
		fmt.Fprintf(out, "  tailscale not installed\n")
	}
	if f.cfPresent {
		fmt.Fprintf(out, "  cloudflared installed\n")
	}
	fmt.Fprintf(out, "\n")

	if !f.shaped {
		// The address change is the whole job, and half-doing it strands the identity provider
		// on the old issuer. Name every step, with the values this box would use.
		name := "partyline.your-domain.com"
		if f.tsName != "" {
			name = f.tsName
		}
		fmt.Fprintf(out, "This install is not tunnel-fronted yet (site %s).\n", f.site)
		fmt.Fprintf(out, "A tunnel changes the instance's ADDRESS, which lives in three .env values, the\n")
		fmt.Fprintf(out, "Caddyfile, and Keycloak's issuer. The installer reconciles all of it:\n\n")
		fmt.Fprintf(out, "  1. In %s/.env set:\n", dir)
		fmt.Fprintf(out, "       SITE_URL=https://%s\n", name)
		fmt.Fprintf(out, "       OIDC_PUBLIC_URL=https://%s/auth\n", name)
		fmt.Fprintf(out, "       OIDC_ISSUER=https://%s/auth/realms/partyline\n", name)
		fmt.Fprintf(out, "  2. rm %s/Caddyfile   (the installer never overwrites one that exists)\n", dir)
		fmt.Fprintf(out, "  3. ptln server install --site https://%s --tls off --bind 127.0.0.1 --http-port %s\n", name, f.httpPort)
		fmt.Fprintf(out, "  4. Run this command again — it prints the tunnel step with the port filled in.\n\n")
		fmt.Fprintf(out, "Signed-in sessions end when the issuer changes; people sign in again. Data is untouched.\n")
		return false
	}

	fmt.Fprintf(out, "This install is tunnel-fronted. Point a tunnel at port %s:\n\n", f.httpPort)
	serveCmd := fmt.Sprintf("tailscale serve --bg --https=443 http://127.0.0.1:%s", f.httpPort)
	fmt.Fprintf(out, "  Tailscale:   %s\n", serveCmd)
	fmt.Fprintf(out, "  Cloudflare:  cloudflared tunnel create partyline && cloudflared tunnel route dns partyline %s\n", strings.TrimPrefix(f.site, "https://"))
	fmt.Fprintf(out, "               then in ~/.cloudflared/config.yml:  service: http://localhost:%s\n\n", f.httpPort)
	if f.bind == "0.0.0.0" {
		fmt.Fprintf(out, "⚠ bound to 0.0.0.0: the plain-HTTP port is reachable from the network, around the\n")
		fmt.Fprintf(out, "  tunnel. Re-run the installer with --bind 127.0.0.1 unless that is intended.\n\n")
	}

	// Offer to run the Tailscale half. Only that half: cloudflared's setup is three commands and
	// an account decision, and running credentialed account operations uninvited is not a favour.
	if f.tsState == "ok" && ops.in != nil {
		fmt.Fprintf(out, "Run the tailscale serve command now? [y/N] ")
		line, err := ops.in.ReadString('\n')
		if err == nil && strings.EqualFold(strings.TrimSpace(line), "y") {
			if res, err := ops.run("", "tailscale", "serve", "--bg", "--https=443", "http://127.0.0.1:"+f.httpPort); err != nil {
				fmt.Fprintf(out, "failed: %s\n", installFirstLine(res))
				return false
			}
			fmt.Fprintf(out, "serving — your instance is at https://%s\n", f.tsName)
		}
	}
	return true
}
