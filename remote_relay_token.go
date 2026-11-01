package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// The Cloudflare deploy token, kept on request so a partyline upgrade can update the relay's app
// without asking for it again. It lives under ~/.partyline (0600), which loop builders cannot read;
// a session running as the person can, as it can a saved Wrangler login. `ptln relay forget-token`
// deletes it.

func relayTokenPath() string { return filepath.Join(relayDir(), "cloudflare-token") }

func savedRelayAPIToken() string {
	b, err := os.ReadFile(relayTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// offerToSaveRelayToken asks, after a deploy that worked with a typed token, whether to keep it.
func offerToSaveRelayToken(token string) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || savedRelayAPIToken() == token {
		return
	}
	fmt.Fprint(os.Stderr, "Save this token so updates deploy without asking? It stays in ~/.partyline, readable only by you. [Y/n] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "" && a != "y" && a != "yes" {
		return
	}
	if err := saveRelayAPIToken(token); err != nil {
		fmt.Fprintln(os.Stderr, "The token was not saved:", err)
		return
	}
	fmt.Fprintln(os.Stderr, "Saved. `ptln relay forget-token` deletes it.")
}

func saveRelayAPIToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("a Cloudflare token is one word with no spaces")
	}
	if err := os.MkdirAll(relayDir(), 0o700); err != nil {
		return err
	}
	return relayPrivateFile(relayTokenPath(), []byte(token+"\n"))
}

func relayForgetToken() error {
	err := os.Remove(relayTokenPath())
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Println("No Cloudflare token was saved.")
		return nil
	}
	if err == nil {
		fmt.Println("Deleted the saved Cloudflare token.")
	}
	return err
}

// relaySourcesDigest identifies the relay and app this binary deploys, so status can tell when the
// deployed app is older than this partyline.
func relaySourcesDigest() string {
	h := sha256.New()
	_ = fs.WalkDir(relaySources, "relay", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := relaySources.ReadFile(path)
		if err == nil {
			fmt.Fprintf(h, "%s\x00%d\x00", path, len(b))
			h.Write(b)
		}
		return err
	})
	return hex.EncodeToString(h.Sum(nil))
}

// relayOutdated: the relay was deployed from other sources than this binary's (or before they
// were recorded).
func relayOutdated(c relayConfig) bool { return c.Sources != relaySourcesDigest() }
