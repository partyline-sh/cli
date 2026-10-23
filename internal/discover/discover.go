// Package discover: how a ptln CLI and a partyline server find each other with no URL typed.
//
// Epic PA slice P2. Two sources, complementary:
//
//	mDNS (_partyline._tcp.local) — the LAN case. The SERVER side runs on the host (containers
//	on a bridge network cannot multicast to the LAN), installed as a small always-on unit by
//	`ptln server install`. Same mechanism as printers and Chromecast: zero configuration,
//	works the moment both machines share a network.
//
//	The tailnet — everything mDNS cannot cross (VPN, other subnets). The CLI asks tailscale
//	for its peers and probes /api/health on the standard ports; a partyline instance answers,
//	nothing else does.
//
// Discovery only names candidates. Connecting still goes through the device flow — finding a
// server on the network must never be the same thing as being let in.
package discover

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

const Service = "_partyline._tcp"

// Instance is one discovered server: the URL to log in to, a human name, and which source
// found it (mdns / tailnet) — shown so an operator can tell a LAN hit from a VPN hit.
type Instance struct {
	URL    string
	Name   string
	Source string
}

// Announce advertises this box's instance on the LAN until the returned stop func is called.
// TXT carries the full site URL — the port and scheme are the install's, not guessable from
// the mDNS port field alone (Caddy answers on whatever the operator mapped).
func Announce(siteURL, name string, port int) (func(), error) {
	host, _ := os.Hostname()
	if name == "" {
		name = host
	}
	svc, err := mdns.NewMDNSService(name, Service, "", "", port, nil, []string{"url=" + siteURL})
	if err != nil {
		return nil, fmt.Errorf("mdns service: %w", err)
	}
	srv, err := mdns.NewServer(&mdns.Config{Zone: svc})
	if err != nil {
		return nil, fmt.Errorf("mdns server: %w", err)
	}
	return func() { _ = srv.Shutdown() }, nil
}

// Browse collects instances advertising on the LAN for the given window.
func Browse(timeout time.Duration) []Instance {
	entries := make(chan *mdns.ServiceEntry, 16)
	done := make(chan []Instance, 1)
	go func() {
		var found []Instance
		seen := map[string]bool{}
		for e := range entries {
			url := ""
			for _, f := range e.InfoFields {
				if v, ok := strings.CutPrefix(f, "url="); ok {
					url = v
				}
			}
			if url == "" || seen[url] {
				continue
			}
			seen[url] = true
			name := strings.TrimSuffix(e.Name, "."+Service+".local.")
			found = append(found, Instance{URL: url, Name: name, Source: "mdns"})
		}
		done <- found
	}()
	_ = mdns.Query(&mdns.QueryParam{
		Service:     Service,
		Timeout:     timeout,
		Entries:     entries,
		DisableIPv6: true,
	})
	close(entries)
	return <-done
}

// ProbePeers checks candidate hosts (tailnet peer IPs or names) for a partyline instance on
// the standard door pair. TLS verification is off ON PURPOSE and ONLY here: self-host boxes
// run Caddy's internal CA, and this is a discovery probe of /api/health — it names candidates,
// it never sends credentials; the login that follows verifies what the instance really is.
func ProbePeers(ctx context.Context, hosts []string, names map[string]string) []Instance {
	client := &http.Client{
		Timeout:   1500 * time.Millisecond,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	var found []Instance
	for _, h := range hosts {
		for _, cand := range []string{"https://" + h, "https://" + h + ":8443"} {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, cand+"/api/health", nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == 200 {
				found = append(found, Instance{URL: cand, Name: names[h], Source: "tailnet"})
				break
			}
		}
	}
	return found
}
