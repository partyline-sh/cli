package main

import (
	"fmt"
	"strconv"
	"strings"
)

// server_install_ports.go — choosing ports when the defaults are taken.
//
// WHAT IT REPLACED. The installer checked 80, 443 and 2222, and if any were busy it printed
//
//	HTTP port 80 is already in use on 0.0.0.0 — pass --http-port to move it, or stop what is there
//
// and stopped. That is a refusal, not an install. The operator then had to guess a free port,
// retype the whole command with three flags, and find out on the next run whether they guessed
// something else that was busy. On a box that already runs anything — which is most boxes — the
// first install could not succeed without at least two round trips.
//
// It asks now: what is holding the port, what is free, and what would you like instead.
//
// `--yes` keeps the old behaviour deliberately. A non-interactive run has nobody to ask, and
// silently moving a service to a port the caller did not choose is worse than stopping.

// portSuggestions are the alternatives offered for each role, in order of preference. Chosen to be
// memorable next to the default rather than merely free: 8080/8443 are the conventional unprivileged
// pair, and 2222 is already the relay's own default.
var portSuggestions = map[string][]int{
	"http":  {8080, 8880, 8000, 8081},
	"https": {8443, 9443, 8444},
	"relay": {2222, 2022, 22022},
}

// firstFreePort returns the first candidate nothing is listening on, then falls back to walking up
// from the last candidate. Zero means nothing in a sane range was free, which the caller reports
// rather than guessing further.
func firstFreePort(bind string, role string, busy func(string, int) bool) int {
	for _, p := range portSuggestions[role] {
		if !busy(bind, p) {
			return p
		}
	}
	start := 8100
	if len(portSuggestions[role]) > 0 {
		start = portSuggestions[role][0] + 1
	}
	for p := start; p < start+200; p++ {
		if !busy(bind, p) {
			return p
		}
	}
	return 0
}

// listenerOn describes what already holds a port, for the operator to recognise. Best-effort: the
// answer is a hint, and an empty string is a fine outcome — the port is busy either way.
//
// ss first (present on every modern Linux and needs no root for the port itself), lsof second
// (macOS). Neither is required.
func listenerOn(ops installOps, port int) string {
	if out, err := ops.run("", "ss", "-tlnp"); err == nil && out != "" {
		if who := matchListener(out, port); who != "" {
			return who
		}
	}
	if out, err := ops.run("", "lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) >= 1 && f[0] != "COMMAND" {
				return f[0]
			}
		}
	}
	return ""
}

// matchListener pulls the process name out of `ss -tlnp` output for one port.
func matchListener(out string, port int) string {
	want := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// The local address is the 4th column: 0.0.0.0:80, [::]:80, 127.0.0.1:80.
		if !strings.HasSuffix(fields[3], want) {
			continue
		}
		// users:(("nginx",pid=123,fd=6)) — the first quoted word is the program.
		if i := strings.Index(line, `users:(("`); i >= 0 {
			rest := line[i+len(`users:(("`):]
			if j := strings.Index(rest, `"`); j > 0 {
				return rest[:j]
			}
		}
		return "something"
	}
	return ""
}

// resolvePortConflicts asks about every wanted port that is already taken, and returns the config
// with the operator's answers applied.
//
// Returns ok=false when it cannot ask — non-interactive, or the operator declined — so the caller
// falls back to reporting the conflict rather than choosing on their behalf.
func resolvePortConflicts(cfg installConfig, ops installOps) (installConfig, bool) {
	if cfg.assumeYes || ops.in == nil {
		return cfg, false
	}

	var taken []wantedPort
	for _, p := range installWantedPorts(cfg) {
		if ops.portBusy(cfg.bind, p.port) {
			taken = append(taken, p)
		}
	}
	if len(taken) == 0 {
		return cfg, true
	}

	fmt.Fprintf(ops.out, "\nSome ports are already in use on %s.\n\n", cfg.bind)
	for _, p := range taken {
		who := listenerOn(ops, p.port)
		if who != "" {
			fmt.Fprintf(ops.out, "  %-5s %-6d in use by %s\n", p.label, p.port, who)
			continue
		}
		fmt.Fprintf(ops.out, "  %-5s %-6d in use\n", p.label, p.port)
	}
	fmt.Fprintf(ops.out, "\nPick a different port for each, or press enter to take the suggestion.\n")
	fmt.Fprintf(ops.out, "Enter 'keep' to use the busy port anyway — the stack will fail to start if\n")
	fmt.Fprintf(ops.out, "whatever holds it is still there.\n\n")

	for _, p := range taken {
		suggested := firstFreePort(cfg.bind, p.flag, ops.portBusy)
		chosen, ok := askPort(ops, p, suggested, cfg.bind)
		if !ok {
			return cfg, false
		}
		switch p.flag {
		case "http":
			cfg.httpPort = chosen
		case "https":
			cfg.httpsPort = chosen
		case "relay":
			cfg.relayPort = chosen
		}
		cfg.explicit[portEnvName(p.flag)] = true
	}
	return cfg, true
}

func portEnvName(flag string) string {
	switch flag {
	case "http":
		return "HTTP_PORT"
	case "https":
		return "HTTPS_PORT"
	case "relay":
		return "RELAY_PORT"
	}
	return ""
}

// askPort prompts until it gets a usable answer. It re-asks on a busy or malformed port rather than
// accepting it, because the whole point is to leave this loop with a port that will actually bind.
func askPort(ops installOps, p wantedPort, suggested int, bind string) (int, bool) {
	for {
		if suggested > 0 {
			fmt.Fprintf(ops.out, "  %s port [%d]: ", p.label, suggested)
		} else {
			fmt.Fprintf(ops.out, "  %s port: ", p.label)
		}
		line, err := ops.in.ReadString('\n')
		if err != nil {
			return 0, false
		}
		answer := strings.TrimSpace(line)

		switch {
		case answer == "" && suggested > 0:
			return suggested, true
		case answer == "":
			fmt.Fprintf(ops.out, "    nothing free was found automatically — enter a port number.\n")
			continue
		case strings.EqualFold(answer, "keep"):
			return p.port, true
		}

		n, err := strconv.Atoi(answer)
		if err != nil || n < 1 || n > 65535 {
			fmt.Fprintf(ops.out, "    %q is not a port number (1-65535).\n", answer)
			continue
		}
		if ops.portBusy(bind, n) {
			fmt.Fprintf(ops.out, "    %d is also in use. Pick another, or 'keep' to use it anyway.\n", n)
			continue
		}
		return n, true
	}
}
