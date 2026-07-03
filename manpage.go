package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
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
