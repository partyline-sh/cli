package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// server_install_menu_edit.go — the field editors behind the setup menu.
//
// Each returns the config to use and whether anything changed. They re-ask on bad input rather than
// accepting it: the point of the menu is to leave it with settings that install.

// ask prints a prompt with the current value in brackets and returns the trimmed answer. An empty
// answer means "keep what is there", which is what makes every field safe to open and close.
func ask(ops installOps, label, current string) (string, bool) {
	if current != "" {
		fmt.Fprintf(ops.out, "\n  %s [%s]: ", label, current)
	} else {
		fmt.Fprintf(ops.out, "\n  %s: ", label)
	}
	line, err := ops.in.ReadString('\n')
	if err != nil {
		return "", false
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		return current, false
	}
	return answer, true
}

func editSite(c installConfig, ops installOps) (installConfig, bool) {
	fmt.Fprintf(ops.out, "\n  The address people will use to reach this instance.\n")
	fmt.Fprintf(ops.out, "  A LAN address or a hostname is fine — it does not need a public domain.\n")
	fmt.Fprintf(ops.out, "  It must be reachable FROM INSIDE the containers, so not localhost.\n")

	// Show the record when the current name does not resolve. This is where an operator is
	// already looking, and "what do I have to create" is the question they are about to ask.
	if c.site != "" && ops.lookup != nil && ops.localIPs != nil {
		if r, addrs := checkSiteDNS(c.site, ops.lookup, ops.localIPs()); r == dnsMissing {
			fmt.Fprintf(ops.out, "\n  %s does not resolve.\n\n  %s\n", c.site, dnsAdvice(c, ops.localIPs()))
		} else if r == dnsResolvesElsewhere {
			fmt.Fprintf(ops.out, "\n  It resolves to %s, which is not this machine.\n"+
				"  That is fine behind a proxy or split-horizon DNS; otherwise the record is stale.\n",
				strings.Join(addrs, ", "))
		}
	}
	answer, changed := ask(ops, "site", c.site)
	if !changed {
		return c, false
	}
	site, err := normalizeSiteURL(answer)
	if err != nil {
		fmt.Fprintf(ops.out, "  %v\n", err)
		return c, false
	}
	if err := rejectLoopbackSite(site); err != nil {
		fmt.Fprintf(ops.out, "  %v\n", err)
		return c, false
	}
	c.site = site
	return c, true
}

func editDir(c installConfig, ops installOps) (installConfig, bool) {
	fmt.Fprintf(ops.out, "\n  Where the stack lives. Nothing here needs root beyond creating it.\n")
	answer, changed := ask(ops, "directory", c.dir)
	if !changed {
		return c, false
	}
	c.dir = strings.TrimRight(answer, "/")
	return c, true
}

func editBind(c installConfig, ops installOps) (installConfig, bool) {
	fmt.Fprintf(ops.out, "\n  Which interface to publish on. 0.0.0.0 is every one.\n")
	fmt.Fprintf(ops.out, "  A LAN or Tailscale address keeps it off the others.\n")
	answer, changed := ask(ops, "interface", c.bind)
	if !changed {
		return c, false
	}
	c.bind = answer
	c.explicit["BIND_ADDR"] = true
	return c, true
}

// editPortField edits one port, suggesting a free one when the current choice is taken.
func editPortField(role string) func(installConfig, installOps) (installConfig, bool) {
	return func(c installConfig, ops installOps) (installConfig, bool) {
		cur := portFor(c, role)
		if ops.portBusy(c.bind, cur) {
			if who := listenerOn(ops, cur); who != "" {
				fmt.Fprintf(ops.out, "\n  %d is held by %s.\n", cur, who)
			} else {
				fmt.Fprintf(ops.out, "\n  %d is in use.\n", cur)
			}
			if free := firstFreePort(c.bind, role, ops.portBusy); free > 0 {
				fmt.Fprintf(ops.out, "  %d is free.\n", free)
			}
		}
		answer, changed := ask(ops, role+" port", strconv.Itoa(cur))
		if !changed {
			return c, false
		}
		n, err := strconv.Atoi(answer)
		if err != nil || n < 1 || n > 65535 {
			fmt.Fprintf(ops.out, "  %q is not a port number (1-65535).\n", answer)
			return c, false
		}
		if ops.portBusy(c.bind, n) {
			fmt.Fprintf(ops.out, "  %d is also in use — the stack would fail to start.\n", n)
			return c, false
		}
		switch role {
		case "http":
			c.httpPort = n
			c.explicit["HTTP_PORT"] = true
		case "https":
			c.httpsPort = n
			c.explicit["HTTPS_PORT"] = true
		case "relay":
			c.relayPort = n
			c.explicit["RELAY_PORT"] = true
		}
		return c, true
	}
}

func editDNS(c installConfig, ops installOps) (installConfig, bool) {
	fmt.Fprintf(ops.out, "\n  A resolver to use instead of the system one. OPTIONAL.\n")
	fmt.Fprintf(ops.out, "  Set it when the site name lives only in an internal zone — the\n")
	fmt.Fprintf(ops.out, "  containers resolve through it too, and the web container has to be\n")
	fmt.Fprintf(ops.out, "  able to resolve the site to reach the identity provider.\n")
	fmt.Fprintf(ops.out, "  Enter 'none' to go back to the system resolver.\n")
	cur := c.dns
	if cur == "" {
		cur = "none"
	}
	answer, changed := ask(ops, "dns", cur)
	if !changed {
		return c, false
	}
	if strings.EqualFold(answer, "none") || answer == "" {
		c.dns = ""
		return c, true
	}
	host := answer
	if h, _, err := net.SplitHostPort(answer); err == nil {
		host = h
	}
	if net.ParseIP(host) == nil {
		fmt.Fprintf(ops.out, "  %q is not an address — a resolver is an IP, optionally with :port.\n", answer)
		return c, false
	}
	c.dns = answer
	return c, true
}

func editTLS(c installConfig, ops installOps) (installConfig, bool) {
	fmt.Fprintf(ops.out, "\n  1  auto      decide from the address (default)\n")
	fmt.Fprintf(ops.out, "  2  acme      Let's Encrypt — needs this box reachable from the internet\n")
	fmt.Fprintf(ops.out, "  3  internal  Caddy's own CA — works offline, browsers warn until trusted\n")
	fmt.Fprintf(ops.out, "  4  off       plain HTTP — nothing encrypted in transit\n")
	answer, changed := ask(ops, "certificate", string(resolveTLSMode(c.tls, c.site)))
	if !changed {
		return c, false
	}
	switch strings.ToLower(answer) {
	case "1", "auto":
		c.tls = tlsAuto
	case "2", "acme":
		c.tls = tlsACME
	case "3", "internal":
		c.tls = tlsInternal
	case "4", "off":
		c.tls = tlsOff
	default:
		fmt.Fprintf(ops.out, "  Pick 1-4.\n")
		return c, false
	}
	return c, true
}

func editEdge(c installConfig, ops installOps) (installConfig, bool) {
	fmt.Fprintf(ops.out, "\n  Run Caddy as the edge, or leave TLS to something already in front.\n")
	cur := "caddy"
	if c.noCaddy {
		cur = "none"
	}
	answer, changed := ask(ops, "edge (caddy/none)", cur)
	if !changed {
		return c, false
	}
	switch strings.ToLower(answer) {
	case "caddy", "yes", "on":
		c.noCaddy = false
	case "none", "no", "off":
		c.noCaddy = true
		c.explicit["CADDY_REPLICAS"] = true
	default:
		fmt.Fprintf(ops.out, "  Enter caddy or none.\n")
		return c, false
	}
	return c, true
}
