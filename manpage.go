package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The man page is embedded so `ptln man` works on EVERY install path (brew cask, curl|sh,
// deb/rpm) regardless of where — or whether — the .1 file landed in the system manpath.
// The deb/rpm/cask ALSO drop it in man1 so system `man ptln` works; this is the guaranteed
// fallback that needs nothing installed.
//
//go:embed docs/partyline.1
var manPage string

// manMain renders the embedded man page (via mandoc/nroff if present, else raw roff).
//
//	ptln man          formatted, paged like man(1)
//	ptln man --raw    the roff source (e.g. `ptln man --raw | sudo tee /usr/local/share/man/man1/ptln.1`)
func manMain(args []string) {
	if len(args) > 0 && (args[0] == "--raw" || args[0] == "-r") {
		fmt.Print(manPage)
		return
	}
	if len(args) > 0 && (args[0] == "install" || args[0] == "--install") {
		manInstall()
		return
	}
	// Prefer a real roff renderer; page through $PAGER/less when attached to a terminal.
	for _, r := range [][]string{{"mandoc"}, {"nroff", "-man"}} {
		if _, err := exec.LookPath(r[0]); err != nil {
			continue
		}
		formatted, err := renderRoff(r[0], r[1:])
		if err != nil || strings.TrimSpace(formatted) == "" {
			continue
		}
		page(formatted)
		return
	}
	fmt.Print(manPage) // no renderer available → raw roff is still readable enough
}

func renderRoff(bin string, args []string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(manPage)
	out, err := cmd.Output()
	return string(out), err
}

// page sends text through the user's pager when stdout is a TTY, else prints it.
func page(text string) {
	fi, _ := os.Stdout.Stat()
	if fi == nil || fi.Mode()&os.ModeCharDevice == 0 { // not a terminal (piped/redirected)
		fmt.Print(text)
		return
	}
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less -R"
	}
	f := strings.Fields(pager)
	cmd := exec.Command(f[0], f[1:]...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if cmd.Run() != nil {
		fmt.Print(text)
	}
}

// manInstall drops the man page into a writable manpath so `man partyline`/`man ptln` work
// after ANY install (the brew cask + deb/rpm do it via packaging; this is the universal
// fallback for the curl|sh installer, and `ptln man install` for anyone). Writes partyline.1
// and a ptln.1 alias (roff `.so` include). Prefers a system manpath, falls back to
// ~/.local/share/man (no sudo) and reminds you to have it on MANPATH.
func manInstall() {
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/opt/homebrew/share/man/man1", // Homebrew (Apple silicon)
		"/usr/local/share/man/man1",    // Homebrew (Intel) / common
		filepath.Join(home, ".local/share/man/man1"),
	}
	for _, dir := range candidates {
		if os.MkdirAll(dir, 0o755) != nil {
			continue
		}
		if os.WriteFile(filepath.Join(dir, "partyline.1"), []byte(manPage), 0o644) != nil {
			continue // not writable (needs sudo) → try the next
		}
		// ptln.1 aliases partyline.1 via a roff source-include (relative to the manpath root).
		_ = os.WriteFile(filepath.Join(dir, "ptln.1"), []byte(".so man1/partyline.1\n"), 0o644)
		fmt.Printf("✓ installed man pages to %s — try: man ptln\n", dir)
		if strings.Contains(dir, home) {
			fmt.Printf("  (if `man ptln` can't find it, add to your shell rc: export MANPATH=\"%s:$MANPATH\")\n", filepath.Dir(dir))
		}
		return
	}
	fmt.Fprintf(os.Stderr, "couldn't write to a manpath without elevated permissions.\n"+
		"install it yourself:  ptln man --raw | sudo tee /usr/local/share/man/man1/partyline.1 >/dev/null\n")
}
