package main

// Reconfiguring an EXISTING install — `ptln server install` re-run against a directory that
// already has one. The pipeline always reconciled (idempotent steps, .env only added to); what
// was missing, and cost a real box a wrong site address it could not correct, was:
//
//   - the menu opening with the install's CURRENT settings rather than defaults
//   - a changed site reaching the .env variables derived from the old one (writeInstallEnvOverrides)
//   - the Caddyfile following a changed config without breaking its own promise that hand
//     edits are never overwritten
//
// The Caddyfile promise is kept with a content hash: when the file was written, its hash went
// into .caddyfile.sha256 beside it. A file that still matches its sidecar is ours-unchanged and
// safe to regenerate; anything else was edited by the operator — it stays, and the config the
// installer WOULD have written lands in Caddyfile.new for them to merge.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const caddyfileHashSidecar = ".caddyfile.sha256"

func contentHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// reconcileCaddyfile writes the Caddyfile for the current config: created when missing,
// regenerated when the existing file is our own unchanged output, preserved (with the new
// config written beside it) when the operator has edited it.
func reconcileCaddyfile(c installConfig, o installOps) error {
	if o.out == nil {
		o.out = io.Discard
	}
	if o.writeFile == nil {
		o.writeFile = os.WriteFile
	}
	if c.noCaddy {
		return nil // no bundled edge — the operator owns whatever fronts this box
	}
	path := filepath.Join(c.dir, "Caddyfile")
	sidecar := filepath.Join(c.dir, caddyfileHashSidecar)
	want := []byte(caddyfileFor(c))

	cur, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := o.writeFile(path, want, 0o644); err != nil {
			return err
		}
		return o.writeFile(sidecar, []byte(contentHash(want)+"\n"), 0o644)
	}
	if err != nil {
		return err
	}
	if string(cur) == string(want) {
		// Current already — but an install that predates the sidecar needs one for next time.
		return o.writeFile(sidecar, []byte(contentHash(want)+"\n"), 0o644)
	}
	recorded, _ := os.ReadFile(sidecar)
	if strings.TrimSpace(string(recorded)) == contentHash(cur) {
		// Ours, unchanged by hand — safe to follow the config.
		fmt.Fprintf(o.out, "(updating the Caddyfile for the new settings) ")
		if err := o.writeFile(path, want, 0o644); err != nil {
			return err
		}
		return o.writeFile(sidecar, []byte(contentHash(want)+"\n"), 0o644)
	}
	// Edited by the operator (or from an install that predates the sidecar, where we cannot
	// tell): the promise in the file's own banner holds. Give them the new config to merge.
	fmt.Fprintf(o.out, "(your Caddyfile has local edits — kept; the new config is in Caddyfile.new) ")
	return o.writeFile(path+".new", want, 0o644)
}

// prefillFromExisting loads an existing install's settings into any field the operator did not
// set on this run's command line, so the setup menu opens showing the box AS IT IS. Values are
// read locally and never printed; the site URL is the one plan-visible exception.
func prefillFromExisting(c installConfig, o installOps) installConfig {
	b, err := os.ReadFile(filepath.Join(c.dir, ".env"))
	if err != nil {
		return c
	}
	text := string(b)
	if c.site == "" {
		c.site = envValue(text, "SITE_URL")
	}
	if !c.explicit["BIND_ADDR"] {
		if v := envValue(text, "BIND_ADDR"); v != "" {
			c.bind = v
		}
	}
	for _, p := range []struct {
		key   string
		field *int
	}{
		{"HTTP_PORT", &c.httpPort},
		{"HTTPS_PORT", &c.httpsPort},
		{"RELAY_PORT", &c.relayPort},
	} {
		if !c.explicit[p.key] {
			if n, err := strconv.Atoi(envValue(text, p.key)); err == nil && n > 0 {
				*p.field = n
			}
		}
	}
	if !c.explicit["MINIO_REPLICAS"] {
		if v := envValue(text, "MINIO_REPLICAS"); v != "" {
			c.minio = v != "0"
		}
	}
	if !c.explicit["CADDY_REPLICAS"] && envValue(text, "CADDY_REPLICAS") == "0" {
		c.noCaddy = true
	}
	// TLS mode isn't in .env — infer it from the Caddyfile we generated.
	if c.tls == "" {
		if cf, err := os.ReadFile(filepath.Join(c.dir, "Caddyfile")); err == nil {
			switch {
			case strings.Contains(string(cf), "tls internal"):
				c.tls = tlsInternal
			case strings.HasPrefix(c.site, "http://"):
				c.tls = tlsOff
			}
		}
	}
	fmt.Fprintf(o.out, "existing install found in %s — its current settings are loaded below; change what you need.\n\n", c.dir)
	return c
}
