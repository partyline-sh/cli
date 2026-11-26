package main

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/grandcat/zeroconf"
	"partyline.sh/partyline/internal/gatewayjoin"
)

const relayDiscoveryService = "_ptln-relay._tcp"

type discoveredRelay struct {
	Name, Origin string
	Addresses    []string
	Port         int
}

func relayDiscoveryInterfaces() ([]net.Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := []net.Interface{}
	for _, iface := range all {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, e := net.ParseCIDR(address.String())
			if e == nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
				out = append(out, iface)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no local multicast network; use a relay invite instead")
	}
	return out, nil
}
func advertiseRelay(name, origin string) (*zeroconf.Server, error) {
	if _, err := newRelayConfig(origin, strings.Repeat("0", 32)); err != nil {
		return nil, err
	}
	ifaces, err := relayDiscoveryInterfaces()
	if err != nil {
		return nil, err
	}
	clean := gatewayjoin.CleanName(name)
	for len(clean) > 60 {
		_, size := utf8.DecodeLastRuneInString(clean)
		clean = clean[:len(clean)-size]
	}
	if len("origin="+origin) > 255 {
		return nil, fmt.Errorf("gateway address is too long for discovery; use an invite")
	}
	instance := strings.NewReplacer(`\`, `\\`, `.`, `\.`, ` `, `\ `).Replace(clean)
	return zeroconf.Register(instance, relayDiscoveryService, "local.", 443, []string{"v=1", "origin=" + origin}, ifaces)
}
func parseDiscoveredRelay(entry *zeroconf.ServiceEntry) (discoveredRelay, bool) {
	var out discoveredRelay
	if entry == nil || len(entry.Text) > 16 || entry.Port != 443 {
		return out, false
	}
	v := ""
	for _, part := range entry.Text {
		if len(part) > 255 {
			return out, false
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch key {
		case "origin":
			if out.Origin != "" {
				return out, false
			}
			out.Origin = value
		case "v":
			v = value
		}
	}
	if v != "1" {
		return out, false
	}
	if _, err := newRelayConfig(out.Origin, strings.Repeat("0", 32)); err != nil {
		return out, false
	}
	name, ok := decodeDiscoveryName(entry.Instance)
	if !ok {
		return out, false
	}
	out.Name = gatewayjoin.CleanName(name)
	out.Port = entry.Port
	for _, ip := range append(entry.AddrIPv4, entry.AddrIPv6...) {
		if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			out.Addresses = append(out.Addresses, ip.String())
		}
	}
	// The address hints are untrusted and never bypass HTTPS certificate checks.
	return out, true
}

func decodeDiscoveryName(value string) (string, bool) {
	out := []byte{}
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			out = append(out, value[i])
			continue
		}
		i++
		if i >= len(value) {
			return "", false
		}
		if i+2 < len(value) && value[i] >= '0' && value[i] <= '9' && value[i+1] >= '0' && value[i+1] <= '9' && value[i+2] >= '0' && value[i+2] <= '9' {
			n := int(value[i]-'0')*100 + int(value[i+1]-'0')*10 + int(value[i+2]-'0')
			if n > 255 {
				return "", false
			}
			out = append(out, byte(n))
			i += 2
		} else {
			out = append(out, value[i])
		}
	}
	return string(out), utf8.Valid(out)
}
func discoverRelays(ctx context.Context) ([]discoveredRelay, error) {
	ifaces, err := relayDiscoveryInterfaces()
	if err != nil {
		return nil, err
	}
	resolver, err := zeroconf.NewResolver(zeroconf.SelectIfaces(ifaces))
	if err != nil {
		return nil, err
	}
	entries := make(chan *zeroconf.ServiceEntry, 32)
	if err = resolver.Browse(ctx, relayDiscoveryService, "local.", entries); err != nil {
		return nil, err
	}
	found := map[string]discoveredRelay{}
	for {
		select {
		case <-ctx.Done():
			out := []discoveredRelay{}
			for _, entry := range found {
				out = append(out, entry)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			return out, nil
		case entry, ok := <-entries:
			if !ok {
				return nil, fmt.Errorf("discovery stopped; use an invite")
			}
			if result, valid := parseDiscoveredRelay(entry); valid && len(found) < 32 {
				found[result.Origin] = result
			}
		}
	}
}
func formatDiscoveredRelays(entries []discoveredRelay) string {
	var out strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&out, "%s\n  %s · unverified\n", entry.Name, entry.Origin)
	}
	if len(entries) == 0 {
		out.WriteString("No private gateways found. The gateway may be offline, or this network may block discovery. Use an invite or set up this computer.\n")
	}
	return out.String()
}
