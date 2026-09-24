package main

// Reading the TLS decision back off an existing install.
//
// The Caddyfile is the only record of how TLS is terminated — there is no setting in .env for it,
// as server_tunnel.go says: "The TLS mode is not stored anywhere; the Caddyfile is the artifact
// that carries the decision."
//
// That made an upgrade dangerous. `ptln server upgrade` re-derives the mode from the site URL,
// and a public name derives to ACME — correct for a box that faces the internet directly, wrong
// for one behind Cloudflare or a tunnel, where the origin must speak plain HTTP. Regenerating
// turned the second into the first, Caddy began redirecting HTTP to HTTPS on the origin, the
// proxy in front followed the redirect straight back, and the site served an infinite loop.
//
// So an upgrade may add routes; it may never change how TLS is terminated. The mode is read back
// from the file it is about to replace.

import (
	"os"
	"path/filepath"
	"strings"
)

// tlsModeOfCaddyfile reports the mode an existing Caddyfile encodes, or "" when there is no file
// to learn from (a fresh install, where deriving from the site URL is right).
func tlsModeOfCaddyfile(dir string) tlsMode {
	b, err := os.ReadFile(filepath.Join(dir, "Caddyfile"))
	if err != nil {
		return ""
	}
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "tls internal"):
			return tlsInternal
		}
		// The first site block's address carries the decision. The INTERNAL door is always
		// http://caddy and says nothing about how the public site is served, so it is skipped.
		if strings.HasSuffix(line, "{") && !strings.HasPrefix(line, "{") {
			addr := strings.TrimSpace(strings.TrimSuffix(line, "{"))
			if addr == "http://caddy" {
				continue
			}
			if strings.HasPrefix(addr, "http://") {
				return tlsOff // plain HTTP origin: something in front terminates TLS
			}
			// An https-or-bare address may still be `tls internal`, which appears a few lines
			// further in, so keep reading rather than concluding ACME here.
		}
	}
	// A bare or https:// address with no `tls internal` is a publicly-issued certificate.
	if strings.Contains(string(b), "{") {
		return tlsACME
	}
	return ""
}

// keepExistingTLSMode pins cfg to the mode the install already uses, unless the operator asked
// for a specific one on this run.
func keepExistingTLSMode(cfg installConfig, ops installOps) installConfig {
	if cfg.tls != "" && cfg.tls != tlsAuto {
		return cfg // an explicit --tls on this run wins
	}
	if mode := tlsModeOfCaddyfile(cfg.dir); mode != "" {
		cfg.tls = mode
	}
	return cfg
}
