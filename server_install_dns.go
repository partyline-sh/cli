package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// server_install_dns.go — does the name people will type actually reach this box?
//
// The installer took a hostname and never checked it. That is the one setting nothing downstream
// can recover from: SITE_URL is what the app builds every link out of, what Keycloak stamps into
// every token's issuer, and what the web container resolves when it fetches OIDC discovery. A name
// that resolves nowhere, or to somebody else's machine, produces an install that passes every step
// and then fails at sign-in — and the error surfaces from a component nobody was thinking about.
//
// So it resolves the name, compares the answer against this machine's own addresses, and says what
// to create when there is nothing there.

// dnsResult is what a lookup found, in the terms the operator needs to act on.
type dnsResult int

const (
	dnsNotChecked        dnsResult = iota // an IP literal, or --tls off with a bare address: nothing to look up
	dnsResolvesHere                       // the name points at an address this machine holds
	dnsResolvesElsewhere                  // it resolves, but not to us — usually a stale record or a proxy
	dnsMissing                            // no record at all
)

// checkSiteDNS resolves the site's host and reports what it found, plus the addresses it saw.
func checkSiteDNS(site string, lookup func(string) ([]string, error), local []string) (dnsResult, []string) {
	_, host, _ := strings.Cut(site, "://")
	host = stripHostPort(strings.TrimRight(host, "/"))
	if host == "" || net.ParseIP(host) != nil {
		// An IP literal is its own answer. Whether it is THIS box is a routing question the
		// port checks already cover.
		return dnsNotChecked, nil
	}

	addrs, err := lookup(host)
	if err != nil || len(addrs) == 0 {
		return dnsMissing, nil
	}
	mine := map[string]bool{}
	for _, a := range local {
		mine[a] = true
	}
	for _, a := range addrs {
		if mine[a] {
			return dnsResolvesHere, addrs
		}
	}
	return dnsResolvesElsewhere, addrs
}

// dnsNote is the one-line status for the menu, next to the site.
func dnsNote(r dnsResult, addrs []string) string {
	switch r {
	case dnsResolvesHere:
		return "resolves here"
	case dnsResolvesElsewhere:
		return "resolves to " + strings.Join(addrs, ", ") + " — NOT this box"
	case dnsMissing:
		return "does not resolve"
	}
	return ""
}

// dnsAdvice says what to create. ONE record in the ordinary case, and saying so matters: an
// operator who thinks a self-hosted instance needs a zone full of entries either over-builds or
// puts it off.
//
// The relay does not need its own name. Joiners reach it at the endpoint the instance registers,
// which is this same host — a second record is only for a relay that lives somewhere else.
func dnsAdvice(cfg installConfig, local []string) string {
	_, host, _ := strings.Cut(cfg.site, "://")
	host = stripHostPort(strings.TrimRight(host, "/"))
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}

	target := "<this machine's address>"
	if len(local) > 0 {
		target = local[0]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "One DNS record, in the zone for %s — either form:\n\n", zoneOf(host))
	fmt.Fprintf(&b, "    %-36s A      %s\n", host, target)
	fmt.Fprintf(&b, "    %-36s CNAME  a-name-that-already-points-here\n\n", host)
	b.WriteString("  A when you have the address; CNAME to alias a name that already resolves to\n")
	b.WriteString("  this box (a dynamic-DNS or Tailscale name, say). One record, not both.\n\n")
	b.WriteString("  That is the whole requirement. The relay does not need its own name — joiners\n")
	b.WriteString("  reach it at the endpoint this instance registers, which is this host.\n")
	if len(local) > 1 {
		b.WriteString("\n  Other addresses this machine holds, if one of them is the right side of your\n  network: ")
		b.WriteString(strings.Join(local[1:], ", "))
		b.WriteString("\n")
	}
	return b.String()
}

// zoneOf is the registrable-looking part of a hostname, for "the zone for X" phrasing. Not a public
// suffix parser and does not need to be — it names the place to go, and the operator knows their
// own domain.
func zoneOf(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// resolverFor returns the lookup to use: the operator's DNS server when they named one, otherwise
// whatever the machine already uses.
//
// A custom resolver is OPTIONAL and exists for one case: a name that lives only in an internal
// zone. The system resolver on the host may already know it while the CONTAINERS do not, which is
// the failure that matters — the web container resolves SITE_URL itself to reach the identity
// provider.
func resolverFor(dns string, fallback func(string) ([]string, error)) func(string) ([]string, error) {
	dns = strings.TrimSpace(dns)
	if dns == "" {
		return fallback
	}
	addr := dns
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "53")
	}
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	}
	return func(host string) ([]string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return r.LookupHost(ctx, host)
	}
}

// dnsOverrideYAML is a compose override giving every service that resolves names the operator's
// resolver. Written only when one is set; compose rejects an empty `dns:` list, and an override
// file that exists but does nothing is worse than no file.
func dnsOverrideYAML(dns string) string {
	return fmt.Sprintf(`# GENERATED by ptln server install — the resolver you chose.
#
# The web container resolves SITE_URL to reach the identity provider, and Caddy resolves it for
# certificates. A name that only exists in an internal zone is visible to the host and NOT to a
# container unless the container is told where to look, which is what this does.
#
# Delete this file to go back to Docker's default resolver.
services:
  web:
    dns: [%s]
  caddy:
    dns: [%s]
  relay:
    dns: [%s]
`, dns, dns, dns)
}

// watchSiteDNS polls the site name until it resolves, and says so.
//
// A one-shot "does not resolve" is the wrong shape for DNS: a record takes minutes to propagate,
// and the operator's only move was to reopen this screen and look again. This watches instead — a
// check every few seconds, a dot per miss so the wait is visibly alive, and it comes back the
// moment the record lands (or gives up after the budget and says it is still watching nothing).
//
// No keyboard-cancel, deliberately. Reading stdin while polling needs a second reader, and a
// goroutine parked on the shared reader would swallow the operator's NEXT menu keystroke when the
// watch ends by resolution. The budget bounds it instead; ctrl-c still aborts the whole installer,
// which writes nothing at this stage.
func watchSiteDNS(c installConfig, ops installOps) string {
	_, host, _ := strings.Cut(c.site, "://")
	host = stripHostPort(strings.TrimRight(host, "/"))
	if host == "" || net.ParseIP(host) != nil {
		return "an IP address needs no DNS record"
	}
	lookup := resolverFor(c.dns, ops.lookup)
	sleep := ops.sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	const every = 5 * time.Second
	const checks = 60 // five minutes
	fmt.Fprintf(ops.out, "\n  Watching for %s (a check every %v, up to %v — ctrl-c aborts the install)\n  ",
		host, every, every*checks)
	for i := 0; i < checks; i++ {
		r, addrs := checkSiteDNS(c.site, lookup, ops.localIPs())
		switch r {
		case dnsResolvesHere:
			fmt.Fprintf(ops.out, "\n")
			return "resolved — " + host + " now points at this box"
		case dnsResolvesElsewhere:
			fmt.Fprintf(ops.out, "\n")
			return "resolved to " + strings.Join(addrs, ", ") + " — NOT this box (a proxy, or the wrong target)"
		}
		fmt.Fprintf(ops.out, ".")
		sleep(every)
	}
	fmt.Fprintf(ops.out, "\n")
	return "still nothing after " + (every * checks).String() + " — check the record was saved, then watch again"
}
