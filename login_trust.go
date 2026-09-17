package main

// The TOFU prompt for a self-hosted instance's own certificate authority. Interactive only —
// a daemon or script must not silently trust; it is told to run `ptln login <url>` once, by a
// human, on this machine.

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/api"
)

func trustInstanceCert(base string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "ptln: %s serves a certificate this machine does not trust, and there is no terminal to confirm on.\n", base)
		fmt.Fprintf(os.Stderr, "      Run `ptln login %s` interactively once to review and pin it.\n", base)
		return false
	}
	chain, err := api.FetchChain(base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln: could not read the certificate %s is serving: %v\n", base, err)
		return false
	}
	fmt.Printf("\n☎ %s serves its own certificate authority (normal for a self-hosted box).\n", base)
	fmt.Printf("  SHA-256 fingerprint:\n    %s\n", api.Fingerprint(chain[0]))
	fmt.Printf("  Compare it on the server:  openssl x509 -in <install-dir>/… -noout -fingerprint -sha256\n")
	fmt.Printf("  Trust this instance? Connections will verify against exactly this certificate from now on. [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if l := strings.ToLower(strings.TrimSpace(line)); l != "y" && l != "yes" {
		fmt.Println("  not trusted — nothing saved.")
		return false
	}
	if err := api.SavePin(chain); err != nil {
		fmt.Fprintf(os.Stderr, "ptln: could not save the pin: %v\n", err)
		return false
	}
	fmt.Println("  pinned. A changed certificate will be refused with this same fingerprint check.")
	return true
}
