package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The monolith incident, as a test: an install created with the wrong site address must be
// correctable by re-running the installer. env-bootstrap only ever adds, so the DERIVED
// variables (issuer among them) kept the old address and sign-in broke.
func TestSiteChangePropagatesToDerivedVars(t *testing.T) {
	dir := t.TempDir()
	env := strings.Join([]string{
		"SITE_URL=https://192.168.0.170:8443",
		"RELAY_API=https://192.168.0.170:8443",
		"OIDC_PUBLIC_URL=https://192.168.0.170:8443/auth",
		"OIDC_ISSUER=https://192.168.0.170:8443/auth/realms/partyline",
		"POSTGRES_PASSWORD=a-secret-that-must-survive",
		"UNRELATED_URL=https://example.com", // not derived from the site — untouched
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cfg := installConfig{
		dir: dir, site: "https://192.168.1.170:8443",
		bind: "0.0.0.0", httpPort: 8080, httpsPort: 8443, relayPort: 2222,
		explicit: map[string]bool{},
	}
	if err := writeInstallEnvOverrides(cfg, installOps{writeFile: os.WriteFile, out: &out}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".env"))
	text := string(got)
	for _, want := range []string{
		"SITE_URL=https://192.168.1.170:8443",
		"RELAY_API=https://192.168.1.170:8443",
		"OIDC_PUBLIC_URL=https://192.168.1.170:8443/auth",
		"OIDC_ISSUER=https://192.168.1.170:8443/auth/realms/partyline", // suffix survives the prefix swap
		"POSTGRES_PASSWORD=a-secret-that-must-survive",
		"UNRELATED_URL=https://example.com",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q after site change; .env names present: %v", want, envFileNames(filepath.Join(dir, ".env")))
		}
	}
	if strings.Contains(text, "192.168.0.170") {
		t.Error("the old address survived somewhere in .env")
	}
	if !strings.Contains(out.String(), "site changed") {
		t.Errorf("the change was silent: %q", out.String())
	}
}

// The Caddyfile promise, kept mechanically: ours-unchanged follows the config; hand-edited
// stays, with the would-be config in Caddyfile.new.
func TestCaddyfileReconcile(t *testing.T) {
	dir := t.TempDir()
	ops := installOps{writeFile: os.WriteFile, out: os.Stderr}
	cfg := installConfig{dir: dir, site: "https://192.168.0.170:8443", tls: tlsInternal, explicit: map[string]bool{}}

	// fresh: file + sidecar
	if err := reconcileCaddyfile(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, caddyfileHashSidecar)); err != nil {
		t.Fatal("no hash sidecar on fresh write")
	}

	// config change on an untouched file: regenerated in place
	cfg.site = "https://192.168.1.170:8443"
	var out strings.Builder
	ops.out = &out
	if err := reconcileCaddyfile(cfg, ops); err != nil {
		t.Fatal(err)
	}
	cf, _ := os.ReadFile(filepath.Join(dir, "Caddyfile"))
	if !strings.Contains(string(cf), "192.168.1.170") {
		t.Error("untouched Caddyfile did not follow the config change")
	}
	if _, err := os.Stat(filepath.Join(dir, "Caddyfile.new")); err == nil {
		t.Error("Caddyfile.new written even though the file was ours-unchanged")
	}

	// hand-edit, then change config again: the edit survives, the new config lands beside it
	edited := string(cf) + "\n# operator tuning: keep me\n"
	if err := os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.site = "https://box.example.com"
	if err := reconcileCaddyfile(cfg, ops); err != nil {
		t.Fatal(err)
	}
	cf2, _ := os.ReadFile(filepath.Join(dir, "Caddyfile"))
	if !strings.Contains(string(cf2), "keep me") {
		t.Error("a hand-edited Caddyfile was overwritten — the banner's promise is broken")
	}
	nw, err := os.ReadFile(filepath.Join(dir, "Caddyfile.new"))
	if err != nil || !strings.Contains(string(nw), "box.example.com") {
		t.Error("the new config was not offered in Caddyfile.new")
	}
}

// Re-running against an existing install opens the menu showing the box AS IT IS.
func TestPrefillFromExisting(t *testing.T) {
	dir := t.TempDir()
	env := "SITE_URL=https://10.0.0.5:9443\nBIND_ADDR=127.0.0.1\nHTTP_PORT=9080\nHTTPS_PORT=9443\nRELAY_PORT=9222\nMINIO_REPLICAS=0\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte("x {\n tls internal\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	c := prefillFromExisting(installConfig{
		dir: dir, bind: "0.0.0.0", httpPort: 80, httpsPort: 443, relayPort: 2222, minio: true,
		explicit: map[string]bool{},
	}, installOps{out: &out})
	if c.site != "https://10.0.0.5:9443" || c.bind != "127.0.0.1" ||
		c.httpPort != 9080 || c.httpsPort != 9443 || c.relayPort != 9222 || c.minio || c.tls != tlsInternal {
		t.Errorf("prefill wrong: %+v", c)
	}
	// an explicit flag from THIS run beats the existing install
	c2 := prefillFromExisting(installConfig{
		dir: dir, httpPort: 7070, explicit: map[string]bool{"HTTP_PORT": true},
	}, installOps{out: &out})
	if c2.httpPort != 7070 {
		t.Errorf("explicit --http-port lost to the existing value: %d", c2.httpPort)
	}
	if !strings.Contains(out.String(), "existing install found") {
		t.Error("prefill was silent")
	}
}
