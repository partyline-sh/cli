package main

// The CLI half of instance discovery (epic PA · P2): mDNS browse for the LAN, tailnet probe
// for everything mDNS cannot cross. Used only by bare `ptln login` when nothing is configured
// and this box hosts no install — the cases where the operator would otherwise be told to go
// find a URL by hand.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"partyline.sh/partyline/internal/discover"
)

func discoverInstances() []discover.Instance {
	fmt.Print("☎ looking for partyline on this network… ")
	found := discover.Browse(2 * time.Second)
	if peers, names := tailnetPeers(); len(peers) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		found = append(found, discover.ProbePeers(ctx, peers, names)...)
		cancel()
	}
	if len(found) == 0 {
		fmt.Println("none found")
	} else {
		fmt.Println()
	}
	return found
}

// tailnetPeers: online peer IPs (and their hostnames) from tailscale, empty when tailscale
// is absent or down. Probing a handful of authenticated VPN peers on two ports is neighborly;
// scanning a subnet would not be, which is why the LAN side is mDNS instead.
func tailnetPeers() ([]string, map[string]string) {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return nil, nil
	}
	var st struct {
		Peer map[string]struct {
			HostName     string   `json:"HostName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
			Online       bool     `json:"Online"`
		} `json:"Peer"`
	}
	if json.Unmarshal(out, &st) != nil {
		return nil, nil
	}
	var hosts []string
	names := map[string]string{}
	for _, p := range st.Peer {
		if !p.Online || len(p.TailscaleIPs) == 0 {
			continue
		}
		ip := p.TailscaleIPs[0]
		hosts = append(hosts, ip)
		names[ip] = p.HostName
	}
	return hosts, names
}

func pickDiscoveredInstance(found []discover.Instance) string {
	if len(found) == 1 {
		f := found[0]
		label := f.URL
		if f.Name != "" {
			label += " (" + f.Name + ")"
		}
		fmt.Printf("  found %s [%s]\nConnect? [Y/n] ", label, f.Source)
		if answerIsNo() {
			return ""
		}
		return f.URL
	}
	fmt.Println("  found:")
	for i, f := range found {
		name := f.Name
		if name != "" {
			name = "  (" + name + ")"
		}
		fmt.Printf("    %d  %s%s  [%s]\n", i+1, f.URL, name, f.Source)
	}
	fmt.Printf("Connect to (1-%d, enter cancels): ", len(found))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	for i, f := range found {
		if line == fmt.Sprintf("%d", i+1) {
			return f.URL
		}
	}
	return ""
}

func answerIsNo() bool {
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	l := strings.ToLower(strings.TrimSpace(line))
	return l == "n" || l == "no"
}
