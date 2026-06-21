// Update awareness: tell users when a newer ptln is out, and how to get it —
// tailored to how THEY installed (Homebrew on mac, or the curl installer on
// Linux/mac). Deliberately quiet and never blocking:
//   - the check is async + throttled (≤ once/24h), cached in ~/.partyline
//   - it never delays a command; a fresh result surfaces on the next run
//   - it's a version-only GET (no token, no user data) and honors opt-out
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

type updateCache struct {
	CheckedAt    time.Time `json:"checked_at"`
	Latest       string    `json:"latest"`
	MinSupported string    `json:"min_supported"`
	Notice       string    `json:"notice"`
}

func updateCachePath() string { return filepath.Join(stateDir(), "version-check.json") }

// updateChecksDisabled honors CI + an explicit opt-out, and skips dev builds.
func updateChecksDisabled() bool {
	if version == "dev" {
		return true
	}
	if os.Getenv("PARTYLINE_NO_UPDATE_CHECK") != "" || os.Getenv("NO_UPDATE_NOTIFIER") != "" {
		return true
	}
	if os.Getenv("CI") != "" {
		return true
	}
	return false
}

func readUpdateCache() updateCache {
	var c updateCache
	if b, err := os.ReadFile(updateCachePath()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

// refreshUpdateCache fetches the latest version and writes the cache. Safe to run
// in a goroutine (the session host lives long enough); cheap and failure-tolerant.
func refreshUpdateCache() {
	latest, minSup, notice, err := api.New().LatestVersion()
	if err != nil || latest == "" {
		return
	}
	b, _ := json.MarshalIndent(updateCache{
		CheckedAt: time.Now(), Latest: latest, MinSupported: minSup, Notice: notice,
	}, "", "  ")
	_ = os.WriteFile(updateCachePath(), b, 0o600)
}

// maybeRefreshUpdateCacheAsync kicks off a background refresh if the cache is
// stale (>24h). Non-blocking; the result is read on a later run. No-op if checks
// are disabled. Callers that run long (the session host) give it time to finish.
func maybeRefreshUpdateCacheAsync() {
	if updateChecksDisabled() {
		return
	}
	if time.Since(readUpdateCache().CheckedAt) < 24*time.Hour {
		return
	}
	go refreshUpdateCache()
}

// notifyIfBehind prints a quiet one-liner (to w, which should be stderr) when a
// newer version is cached. Install-method-aware so Linux/curl users never see a
// brew-only message. No-op if up to date or checks are disabled.
func notifyIfBehind(w io.Writer) {
	if updateChecksDisabled() {
		return
	}
	c := readUpdateCache()
	if c.Latest == "" || !versionLess(version, c.Latest) {
		return
	}
	dim := func(s string) string { return "\x1b[38;5;245m" + s + "\x1b[0m" }
	fmt.Fprintf(w, "%s\n", dim(fmt.Sprintf("↑ ptln %s is available (you're on %s)", c.Latest, version)))
	if c.Notice != "" {
		fmt.Fprintf(w, "%s\n", dim("  "+c.Notice))
	}
	fmt.Fprintf(w, "%s\n", dim("  upgrade: "+upgradeHint()+"   ·   or run: ptln upgrade"))
}

// upgradeHint returns the install-appropriate upgrade command. Homebrew installs
// (mac, or Linuxbrew) → `brew upgrade`; everything else (the curl installer, all
// distro Linux) → re-run the installer.
func upgradeHint() string {
	if installedViaBrew() {
		return "brew upgrade partyline"
	}
	return "curl -fsSL https://partyline.sh/install.sh | sh"
}

// installedViaBrew resolves the running binary (through the `ptln` symlink) and
// reports whether it's actually Homebrew-managed.
func installedViaBrew() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return brewManaged(exe)
}

// brewManaged reports whether a RESOLVED binary path is a Homebrew keg. Homebrew
// always installs real files under a Cellar (mac Intel /usr/local/Cellar, Apple
// Silicon /opt/homebrew/Cellar, Linuxbrew …/.linuxbrew/Cellar) and symlinks them
// onto PATH — so the keg signal is "/Cellar/" in the real path, NOT the prefix.
// The prefix was the bug: the curl installer drops a PLAIN binary into
// /opt/homebrew/bin (or /usr/local/bin), which shares the prefix but isn't
// brew-managed — so a curl install on Apple Silicon wrongly upgraded via brew.
func brewManaged(realPath string) bool {
	return strings.Contains(realPath, "/Cellar/")
}

// upgradeMain is `ptln upgrade` — explicit, never silent. Runs the right path for
// how it was installed (brew vs the curl installer), inheriting the terminal.
func upgradeMain() {
	var cmd *exec.Cmd
	if installedViaBrew() {
		fmt.Println("☎ upgrading via Homebrew…")
		cmd = exec.Command("brew", "upgrade", "partyline")
	} else {
		fmt.Println("☎ upgrading via the installer…")
		cmd = exec.Command("sh", "-c", "curl -fsSL https://partyline.sh/install.sh | sh")
	}
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("upgrade failed: %w\n  try manually: %s", err, upgradeHint()))
	}
}

// versionMain prints the version and does a synchronous, bounded check (the user
// explicitly asked) so it can say up-to-date / behind right now.
func versionMain() {
	fmt.Printf("ptln (partyline) %s\n", version)
	if updateChecksDisabled() {
		return
	}
	if latest, _, _, err := api.New().LatestVersion(); err == nil && latest != "" {
		// refresh the cache opportunistically while we have the answer
		b, _ := json.MarshalIndent(updateCache{CheckedAt: time.Now(), Latest: latest}, "", "  ")
		_ = os.WriteFile(updateCachePath(), b, 0o600)
		if versionLess(version, latest) {
			fmt.Printf("↑ %s is available — upgrade: %s\n", latest, upgradeHint())
		} else {
			fmt.Println("✓ up to date")
		}
	}
}

// versionLess reports whether a < b for dotted numeric versions ("0.1.40").
// A leading 'v' is tolerated; non-numeric/"dev" sorts as oldest so it always
// looks "behind" a real release (but dev is excluded from checks anyway).
func versionLess(a, b string) bool {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func parseVer(s string) [3]int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var out [3]int
	for i, part := range strings.SplitN(s, ".", 3) {
		if i > 2 {
			break
		}
		out[i], _ = strconv.Atoi(strings.TrimFunc(part, func(r rune) bool { return r < '0' || r > '9' }))
	}
	return out
}
