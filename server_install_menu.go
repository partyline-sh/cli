package main

import (
	"fmt"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/brand"
)

// server_install_menu.go — the setup menu. Every choice in one place, before anything is checked.
//
// WHAT IT REPLACED, TWICE. First the installer refused on a busy port and told you to retype the
// command with flags. Then it asked about ports only, one at a time — and even that never ran,
// because it sat after the preflight report and preflight had already returned. An operator saw
//
//	HTTP port 80 is already in use on 0.0.0.0 — pass --http-port (8080 is free)
//	Fix those and run again. Nothing was written.
//
// which is the same refusal with a better hint. Being told what to retype is not the same as being
// asked.
//
// So: one screen, every setting, edit any of them in any order, install when you are ready. It runs
// FIRST — before preflight — because a setting you are about to change should not be reported as a
// problem. `--yes` skips it entirely; a non-interactive run has nobody to ask, and that is the only
// mode where a flag you did not pass becomes an error.

// menuField is one editable line.
type menuField struct {
	label string
	value func(installConfig) string // rendered value
	note  func(installConfig, installOps) string
	edit  func(installConfig, installOps) (installConfig, bool)
}

func installMenuFields() []menuField {
	return []menuField{
		{
			label: "site",
			value: func(c installConfig) string {
				if c.site == "" {
					return "(not set — required)"
				}
				return c.site
			},
			note: func(c installConfig, ops installOps) string {
				if c.site == "" {
					return "the address people will use"
				}
				// Resolution is the one thing about the site nothing downstream can
				// recover from: SITE_URL is what every link, every token issuer and the
				// container's own OIDC discovery are built from.
				if ops.lookup == nil || ops.localIPs == nil {
					return ""
				}
				r, addrs := checkSiteDNS(c.site, resolverFor(c.dns, ops.lookup), ops.localIPs())
				return dnsNote(r, addrs)
			},
			edit: editSite,
		},
		{
			label: "directory",
			value: func(c installConfig) string { return c.dir },
			note: func(c installConfig, ops installOps) string {
				if err := unixWritable(parentOf(c.dir)); err != nil {
					return "cannot write " + parentOf(c.dir)
				}
				return ""
			},
			edit: editDir,
		},
		{
			label: "interface",
			value: func(c installConfig) string { return c.bind },
			note:  func(installConfig, installOps) string { return "" },
			edit:  editBind,
		},
		{
			label: "http port",
			value: func(c installConfig) string { return strconv.Itoa(c.httpPort) },
			note:  portNote("http"),
			edit:  editPortField("http"),
		},
		{
			label: "https port",
			value: func(c installConfig) string { return strconv.Itoa(c.httpsPort) },
			note:  portNote("https"),
			edit:  editPortField("https"),
		},
		{
			label: "relay port",
			value: func(c installConfig) string { return strconv.Itoa(c.relayPort) },
			note:  portNote("relay"),
			edit:  editPortField("relay"),
		},
		{
			label: "dns",
			value: func(c installConfig) string {
				if strings.TrimSpace(c.dns) == "" {
					return "system resolver"
				}
				return c.dns
			},
			note: func(c installConfig, _ installOps) string {
				if strings.TrimSpace(c.dns) == "" {
					return "optional — set one for an internal-only name"
				}
				return "containers resolve through this too"
			},
			edit: editDNS,
		},
		{
			label: "certificate",
			value: func(c installConfig) string { return string(resolveTLSMode(c.tls, c.site)) },
			note: func(c installConfig, _ installOps) string {
				switch resolveTLSMode(c.tls, c.site) {
				case tlsACME:
					return "Let's Encrypt — needs this box reachable from the internet"
				case tlsInternal:
					return "Caddy's own CA — works offline, browsers warn until trusted"
				case tlsOff:
					return "plain HTTP — nothing encrypted in transit"
				}
				return ""
			},
			edit: editTLS,
		},
		{
			label: "edge",
			value: func(c installConfig) string {
				if c.noCaddy {
					return "not running"
				}
				return "Caddy"
			},
			note: func(c installConfig, _ installOps) string {
				if c.noCaddy {
					return "something else terminates TLS"
				}
				return ""
			},
			edit: editEdge,
		},
	}
}

func parentOf(dir string) string {
	d := strings.TrimRight(dir, "/")
	if i := strings.LastIndex(d, "/"); i > 0 {
		return d[:i]
	}
	return "/"
}

// portNote reports a conflict on the line itself, so the menu shows the problem where the fix is.
func portNote(role string) func(installConfig, installOps) string {
	return func(c installConfig, ops installOps) string {
		if c.noCaddy && role != "relay" {
			return "not published (no edge)"
		}
		port := portFor(c, role)
		if !ops.portBusy(c.bind, port) {
			return ""
		}
		if who := listenerOn(ops, port); who != "" {
			return fmt.Sprintf("IN USE by %s", who)
		}
		return "IN USE"
	}
}

func portFor(c installConfig, role string) int {
	switch role {
	case "http":
		return c.httpPort
	case "https":
		return c.httpsPort
	}
	return c.relayPort
}

// runSetupMenu shows every setting and edits them until the operator installs or quits.
//
// It draws with the SAME components as every other menu in the CLI — cgBox's centred rounded frame,
// cgRow's keyed rows, brand.HintBar's footer — rather than printing its own table. A setup screen
// that looks nothing like the rest of the product is the first thing a new operator sees, and it was
// plain fmt.Fprintf lines before this.
func runSetupMenu(cfg installConfig, ops installOps) (installConfig, bool) {
	if cfg.assumeYes || ops.in == nil {
		return cfg, true // nobody to ask; the flags are the answer
	}

	for {
		fields := installMenuFields()
		lines := make([]string, 0, len(fields)+4)
		for i, f := range fields {
			// One column for the setting, one for its value, so the eye runs down the values
			// rather than hunting for them. brand.PadTo, not %-12s: a value can carry a glyph
			// whose display width is not its byte length.
			label := brand.PadTo(f.label, 12) + brand.PadTo(f.value(cfg), 26)
			lines = append(lines, cgRow(strconv.Itoa(i+1), label, f.note(cfg, ops)))
		}

		ready, why := menuReady(cfg, ops)
		lines = append(lines, "")
		if ready {
			lines = append(lines, "  "+brand.HintBar("setup", []brand.Hint{
				{Key: "1-" + strconv.Itoa(len(fields)), Label: "change"},
				{Key: "⏎", Label: "install"},
				{Key: "q", Label: "quit"},
			}, 0))
		} else {
			lines = append(lines, "  "+cgDim+why+cgOff)
			lines = append(lines, "  "+brand.HintBar("setup", []brand.Hint{
				{Key: "1-" + strconv.Itoa(len(fields)), Label: "change"},
				{Key: "q", Label: "quit"},
			}, 0))
		}
		renderSetupBox(ops, "self-host install", lines)

		line, err := ops.in.ReadString('\n')
		if err != nil {
			return cfg, false
		}
		answer := strings.TrimSpace(line)

		switch {
		case answer == "" && ready:
			return cfg, true
		case answer == "":
			fmt.Fprintf(ops.out, "\n  %s\n", why)
			continue
		case strings.EqualFold(answer, "q"), strings.EqualFold(answer, "quit"):
			return cfg, false
		}

		n, convErr := strconv.Atoi(answer)
		if convErr != nil || n < 1 || n > len(fields) {
			fmt.Fprintf(ops.out, "\n  Enter a number between 1 and %d, or press enter.\n", len(fields))
			continue
		}
		updated, ok := fields[n-1].edit(cfg, ops)
		if ok {
			cfg = updated
		}
	}
}

// renderSetupBox draws the frame. Injected through ops.out rather than calling cgBox directly so a
// test can read what was drawn — cgBox writes to os.Stdout, which a test cannot capture, and the
// menu's contents are exactly what the tests assert.
var renderSetupBox = func(ops installOps, title string, lines []string) {
	if ops.out == nil {
		return
	}
	cols, rows := cgTermSize()
	all := append([]string{cgBold + "☎  " + title + cgOff, ""}, lines...)
	fmt.Fprint(ops.out, cgPaintLines(all, cgCursorPark, 0, cols, rows))
}

// menuReady reports whether the settings are installable, and if not, the one thing to fix. It
// names a single blocker rather than a list: the menu is a loop, and the next pass shows the next
// one if there is one.
func menuReady(cfg installConfig, ops installOps) (bool, string) {
	if cfg.site == "" {
		return false, "Set the site address first — everything else is derived from it."
	}
	for _, p := range installWantedPorts(cfg) {
		if ops.portBusy(cfg.bind, p.port) {
			free := firstFreePort(cfg.bind, p.flag, ops.portBusy)
			if free > 0 {
				return false, fmt.Sprintf("%s port %d is in use — %d is free.", p.label, p.port, free)
			}
			return false, fmt.Sprintf("%s port %d is in use.", p.label, p.port)
		}
	}
	if err := unixWritable(parentOf(cfg.dir)); err != nil {
		return false, "Cannot write " + parentOf(cfg.dir) + " — pick a directory you own."
	}
	if ops.lookup != nil && ops.localIPs != nil {
		switch r, _ := checkSiteDNS(cfg.site, resolverFor(cfg.dns, ops.lookup), ops.localIPs()); r {
		case dnsMissing:
			return false, "That name does not resolve — press 1 to see the record to create."
		case dnsResolvesElsewhere:
			// Not a hard block: a proxy or a split-horizon setup legitimately answers
			// elsewhere. Said once, and installing anyway is allowed.
			return true, ""
		}
	}
	return true, ""
}
