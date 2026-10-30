// Update awareness: tell users when a newer ptln is out, and how to get it —
// tailored to how THEY installed (Homebrew on mac, or the curl installer on
// Linux/mac). Deliberately quiet and never blocking:
//   - the check is async + throttled (≤ once/24h), cached in ~/.partyline
//   - it never delays a command; a fresh result surfaces on the next run
//   - it's one unauthenticated GET to GitHub's latest-release endpoint for partyline-sh/cli, where
//     releases are published; it sends nothing about you and honors opt-out
//
// It used to ask partyline.sh/api/v1/version. That endpoint went with the server, so every check
// failed quietly and nobody was ever told a newer version existed.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type updateCache struct {
	CheckedAt    time.Time `json:"checked_at"`
	Latest       string    `json:"latest"`
	MinSupported string    `json:"min_supported"`
	Notice       string    `json:"notice"`
	Changes      []string  `json:"changes,omitempty"` // the latest release's changelog, one line per change
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
	latest, notes, err := latestRelease()
	if err != nil || latest == "" {
		return
	}
	writeUpdateCache(latest, notes)
}

func writeUpdateCache(latest, notes string) {
	b, _ := json.MarshalIndent(updateCache{CheckedAt: time.Now(), Latest: latest, Changes: releaseChanges(notes)}, "", "  ")
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

// watchForUpdates is the watcher's tick step: at most once an hour it looks at the cache, and
// when the cache is a day old it asks GitHub for the latest release in the background. The
// watcher is the one long-lived process, so this is where the daily check lives now.
func watchForUpdates() {
	if time.Since(lastUpdateLook) < time.Hour {
		return
	}
	lastUpdateLook = time.Now()
	maybeRefreshUpdateCacheAsync()
}

var lastUpdateLook time.Time

// updateAvailable is the launcher's notice when the cached latest release is newer than this
// binary, or "": "ptln 0.109.0 is available (you're on 0.108.0) · ptln upgrade".
func updateAvailable() string {
	if updateChecksDisabled() {
		return ""
	}
	c := readUpdateCache()
	if c.Latest == "" || !versionLess(version, c.Latest) {
		return ""
	}
	return fmt.Sprintf("↑ ptln %s is available (you're on %s) · ptln upgrade", c.Latest, version)
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
	fmt.Fprintf(w, "%s\n", dim(fmt.Sprintf("↑ ptln %s is available (you're on %s)", c.Latest, version)))
	if c.Notice != "" {
		fmt.Fprintf(w, "%s\n", dim("  "+c.Notice))
	}
	fmt.Fprintf(w, "%s\n", dim("  run: ptln upgrade"))
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

// brewManaged reports whether a RESOLVED binary path is Homebrew-managed. Homebrew
// always installs real files under a prefix-relative store and symlinks them onto
// PATH, so the signal is that store in the real path, NOT the prefix:
//
//	/Cellar/    formulae   (mac Intel /usr/local, Apple Silicon /opt/homebrew, Linuxbrew)
//	/Caskroom/  casks      — which is what partyline actually ships (see .goreleaser.yaml
//	                        homebrew_casks); a cask is NOT a keg, so matching only
//	                        "/Cellar/" reported every `brew install partyline-sh/tap/partyline`
//	                        as a curl install and made `ptln upgrade` re-run the install
//	                        script OVER the cask's binaries, leaving brew's metadata lying.
//
// The prefix was the original bug: the curl installer drops a PLAIN binary into
// /opt/homebrew/bin (or /usr/local/bin), which shares the prefix but isn't
// brew-managed — so a curl install on Apple Silicon wrongly upgraded via brew.
func brewManaged(realPath string) bool {
	return strings.Contains(realPath, "/Cellar/") || strings.Contains(realPath, "/Caskroom/")
}

// upgradeMain is `ptln upgrade` — explicit, never silent. Runs the right path for
// how it was installed (brew vs the curl installer), inheriting the terminal.
func upgradeMain() {
	if installedViaBrew() {
		fmt.Println("☎ upgrading via Homebrew…")
	} else {
		fmt.Println("☎ upgrading via the installer…")
	}
	cmd := upgradeCommand()
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
	if latest, notes, err := latestRelease(); err == nil && latest != "" {
		writeUpdateCache(latest, notes) // refresh the cache while we have the answer
		if versionLess(version, latest) {
			fmt.Printf("↑ %s is available — run: ptln upgrade\n", latest)
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

// upgradeCommand runs the upgrade the same way the binary was installed. The install script is
// told this binary's directory: left to choose, it takes the first writable directory on PATH,
// which can install a second copy while this one stays first on PATH.
func upgradeCommand() *exec.Cmd {
	if installedViaBrew() {
		return exec.Command("brew", "upgrade", "partyline")
	}
	cmd := exec.Command("sh", "-c", "curl -fsSL https://partyline.sh/install.sh | sh")
	cmd.Env = append(os.Environ(), "PARTYLINE_INSTALL_DIR="+installDir())
	return cmd
}

// installDir is the directory of the running binary, through any symlink.
func installDir() string {
	exe := selfExe()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Dir(exe)
}

// releasesLatestURL is where the latest published release is read from.
var releasesLatestURL = "https://api.github.com/repos/partyline-sh/cli/releases/latest"

// latestRelease returns the newest published release's version, without the "v", and its notes.
func latestRelease() (string, string, error) {
	req, err := http.NewRequest("GET", releasesLatestURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("release check: status %d", res.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&rel); err != nil {
		return "", "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v"), rel.Body, nil
}
