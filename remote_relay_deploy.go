package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/term"
)

// Embed only deploy inputs: never node_modules, local state, or a prebuilt bundle.
//
//go:embed relay/src/index.ts relay/src/noise.ts relay/src/pairing.ts relay/src/session.ts relay/src/app.ts relay/public relay/deploy/package.json relay/deploy/package-lock.json relay/wrangler.jsonc
var relaySources embed.FS

func relayExtract(dir string) error {
	return fs.WalkDir(relaySources, "relay", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(path, "relay/")
		if path == "relay" || path == "relay/deploy" {
			return os.MkdirAll(dir, 0700)
		}
		relative = strings.TrimPrefix(relative, "deploy/")
		dest := filepath.Join(dir, relative)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		b, err := relaySources.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, b, 0600)
	})
}
func relaySavedLogin() bool {
	home, _ := os.UserHomeDir()
	// Wrangler's platform config directory, plus its older ~/.wrangler location.
	dirs := []string{filepath.Join(home, ".wrangler"), filepath.Join(home, ".config", ".wrangler"), filepath.Join(home, "Library", "Preferences", ".wrangler")}
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" {
		dirs = append(dirs, filepath.Join(x, ".wrangler"))
	}
	for _, d := range dirs {
		matches, _ := filepath.Glob(filepath.Join(d, "config", "*.toml"))
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err == nil && regexp.MustCompile(`(?m)^\s*(oauth_token|refresh_token|api_token)\s*=\s*["'][^"'\r\n]+["']`).Match(data) {
				return true
			}
		}
	}
	return false
}
func relayAPIToken() (string, error) {
	if relaySavedLogin() {
		fmt.Fprintln(os.Stderr, "A saved Wrangler login is present; programs running as you may be able to redeploy your relay.")
	}
	token := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN"))
	if token == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", errors.New("set CLOUDFLARE_API_TOKEN or run deploy in a terminal")
		}
		fmt.Fprint(os.Stderr, "Cloudflare API token (used once, never saved): ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return "", errors.New("empty Cloudflare API token")
	}
	return token, nil
}
func relayDeploy(ctx context.Context, domain string) error {
	domain = strings.ToLower(domain)
	if domain != "" {
		c, err := newRelayConfig("https://"+domain, strings.Repeat("0", 32))
		if err != nil || c.RPID != domain || !strings.Contains(domain, ".") {
			return errors.New("--domain requires a DNS host, without a scheme, port or path")
		}
	}
	c, token, err := loadRelay()
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// Missing token beside an existing configuration is not a new registration.
		if _, statErr := os.Stat(filepath.Join(relayDir(), "relay.json")); !os.IsNotExist(statErr) {
			return errors.New("existing relay configuration has no readable token")
		}
		c.MachineID, err = relayRandom(16)
		if err != nil {
			return err
		}
		token, err = relayRandom(32)
		if err != nil {
			return err
		}
	}
	apiToken, err := relayAPIToken()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "ptln-relay-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err = relayExtract(dir); err != nil {
		return err
	}
	b, err := relayDeploymentConfig(domain, c.MachineID, token)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "wrangler.jsonc"), b, 0600); err != nil {
		return err
	}
	// npm does not need the deploy credential. Only Wrangler inherits it.
	cleanEnv := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CLOUDFLARE_API_TOKEN=") && !strings.HasPrefix(v, "CLOUDFLARE_API_KEY=") && !strings.HasPrefix(v, "CLOUDFLARE_EMAIL=") {
			cleanEnv = append(cleanEnv, v)
		}
	}
	npm := exec.CommandContext(ctx, "npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund")
	npm.Dir = dir
	npm.Env = cleanEnv
	npm.Stdout = os.Stderr
	npm.Stderr = os.Stderr
	if err = npm.Run(); err != nil {
		return fmt.Errorf("npm ci: %w", err)
	}
	build := exec.CommandContext(ctx, "npm", "run", "build:pwa")
	build.Dir = dir
	build.Env = cleanEnv
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err = build.Run(); err != nil {
		return fmt.Errorf("build PWA: %w", err)
	}
	digests, err := relayAssetDigests(filepath.Join(dir, "dist"))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "npx", "--no-install", "wrangler", "deploy")
	cmd.Dir = dir
	cmd.Env = append(cleanEnv, "CLOUDFLARE_API_TOKEN="+apiToken, "CI=true", "NO_COLOR=1")
	// Wrangler uses this to select the account when a scoped token can reach several.
	if account := strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")); account != "" {
		cmd.Env = append(cmd.Env, "CLOUDFLARE_ACCOUNT_ID="+account)
	}
	var output bytes.Buffer
	combined := io.MultiWriter(os.Stderr, &output)
	cmd.Stdout = combined
	cmd.Stderr = combined
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("wrangler deploy: %w", err)
	}
	origin := "https://" + domain
	if domain == "" {
		match, matchErr := relayWorkersOrigin(output.String())
		if matchErr != nil {
			return matchErr
		}
		if match == "" {
			return errors.New("deployed, but Wrangler did not report a workers.dev URL; local config was not changed")
		}
		origin = match
	}
	c, err = newRelayConfig(origin, c.MachineID)
	if err != nil {
		return err
	}
	c.Assets = digests
	if err = saveRelay(c, token); err != nil {
		return err
	}
	ensureWatcher()
	fmt.Fprintf(os.Stdout, "Relay: %s\nRP ID: %s\n", c.URL, c.RPID)
	return nil
}

// Bootstrap includes only a token digest, and exists before the first public request.
func relayDeploymentConfig(domain, id, token string) ([]byte, error) {
	b, err := relaySources.ReadFile("relay/wrangler.jsonc")
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err = json.Unmarshal(b, &config); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(token))
	config["vars"] = map[string]string{"INITIAL_MACHINE": id, "INITIAL_HASH": hex.EncodeToString(digest[:])}
	if domain != "" {
		config["workers_dev"] = false
		config["routes"] = []map[string]any{{"pattern": domain, "custom_domain": true}}
	}
	return json.MarshalIndent(config, "", "  ")
}

// Fail closed when human-readable output contains conflicting deployment origins.
func relayWorkersOrigin(output string) (string, error) {
	matches := regexp.MustCompile(`https://[a-zA-Z0-9.-]+\.workers\.dev\b`).FindAllString(output, -1)
	origin := ""
	for _, match := range matches {
		match = strings.ToLower(match)
		if origin != "" && origin != match {
			return "", errors.New("deployed, but Wrangler reported multiple workers.dev URLs; local config was not changed")
		}
		origin = match
	}
	return origin, nil
}
