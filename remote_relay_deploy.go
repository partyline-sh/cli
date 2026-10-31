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
// Every source file, not a hand-kept list: the phone app's modules grew past one, and a missing
// file only failed at deploy time. The scripts are what build:pwa runs after esbuild.
//
//go:embed relay/src/*.ts relay/scripts/version-shell.mjs relay/public relay/deploy/package.json relay/deploy/package-lock.json relay/wrangler.jsonc
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

// relayAPIToken is the deploy token: CLOUDFLARE_API_TOKEN, else the saved one, else asked for.
// typed reports that it was asked for, so the deploy can offer to save it.
func relayAPIToken() (token string, typed bool, err error) {
	if relaySavedLogin() {
		fmt.Fprintln(os.Stderr, "A saved Wrangler login is present; programs running as you may be able to redeploy your relay.")
	}
	token = strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN"))
	if token == "" {
		if token = savedRelayAPIToken(); token != "" {
			fmt.Fprintln(os.Stderr, "Using the saved Cloudflare token (`ptln relay forget-token` deletes it).")
			return token, false, nil
		}
	}
	if token == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return "", false, errors.New("set CLOUDFLARE_API_TOKEN or run deploy in a terminal")
		}
		fmt.Fprint(os.Stderr, "Cloudflare API token: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", false, err
		}
		token, typed = strings.TrimSpace(string(b)), true
	}
	if token == "" {
		return "", false, errors.New("empty Cloudflare API token")
	}
	return token, typed, nil
}

// relayDeploy deploys the relay, or updates it, on the account's workers.dev subdomain.
func relayDeploy(ctx context.Context) error {
	lock, err := lockRelayDeploy()
	if err != nil {
		return err
	}
	defer lock.Close()
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
	apiToken, typed, err := relayAPIToken()
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
	b, err := relayDeploymentConfig(c.MachineID, token)
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
	origin, err := relayWorkersOrigin(output.String())
	if err != nil {
		return err
	}
	if origin == "" {
		return errors.New("deployed, but Wrangler did not report a workers.dev URL; local config was not changed")
	}
	c, err = newRelayConfig(origin, c.MachineID)
	if err != nil {
		return err
	}
	c.Assets, c.Sources, c.Version = digests, relaySourcesDigest(), version
	if err = saveRelay(c, token); err != nil {
		return err
	}
	if typed {
		offerToSaveRelayToken(apiToken)
	}
	ensureWatcher()
	fmt.Fprintf(os.Stdout, "Relay: %s\nRP ID: %s\n", c.URL, c.RPID)
	return nil
}

// Bootstrap includes only a token digest, and exists before the first public request.
func relayDeploymentConfig(id, token string) ([]byte, error) {
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
