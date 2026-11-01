package main

import (
	"context"
	"flag"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

var liveRelayDiscovery = flag.Bool("live-relay-discovery", false, "exercise local multicast discovery with a temporary QA advertisement")

func TestLiveRelayDiscovery(t *testing.T) {
	if !*liveRelayDiscovery {
		t.Skip("explicit live multicast check")
	}
	name := "Partyline QA test gateway"
	origin := "https://partyline-qa.invalid"
	advertisement, err := advertiseRelay(name, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer advertisement.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	entries, err := discoverRelays(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Origin == origin {
			t.Logf("QA display name: %q", entry.Name)
		}
		if entry.Origin == origin && entry.Name == name {
			t.Log("temporary QA gateway discovered via real multicast")
			return
		}
	}
	t.Fatal("local multicast did not return the QA advertisement; manual invite remains the fallback")
}

func TestDiscoveryDecodesDNSLabelsBeforeRendering(t *testing.T) {
	for encoded, want := range map[string]string{`Studio\032gateway`: "Studio gateway", `Studio\ gateway`: "Studio gateway", `MacBook\.local`: "MacBook.local"} {
		got, ok := decodeDiscoveryName(encoded)
		if !ok || got != want {
			t.Fatalf("%q decoded as %q", encoded, got)
		}
	}
	for _, bad := range []string{`bad\`, `bad\999`, `bad\255`} {
		if _, ok := decodeDiscoveryName(bad); ok {
			t.Fatal("invalid DNS display name accepted")
		}
	}
}

func TestDiscoveryIsBoundedAndNeverTrustsAdvertisements(t *testing.T) {
	entry := &zeroconf.ServiceEntry{ServiceRecord: zeroconf.ServiceRecord{Instance: "Studio\x1b\n gateway"}, Port: 443, Text: []string{"v=1", "origin=https://relay.example.net"}, AddrIPv4: []net.IP{net.ParseIP("192.168.1.24"), net.ParseIP("203.0.113.4")}}
	result, ok := parseDiscoveredRelay(entry)
	if !ok || result.Name != "Studio gateway" || len(result.Addresses) != 1 {
		t.Fatalf("invalid parsed gateway: %+v", result)
	}
	if !strings.Contains(formatDiscoveredRelays([]discoveredRelay{result}), "unverified") {
		t.Fatal("discovery presented as verified")
	}
	for _, origin := range []string{"http://relay.test", "https://user:password@relay.test", "https://relay.test/path", "https://relay.test?token=secret"} {
		entry.Text = []string{"v=1", "origin=" + origin}
		if _, ok := parseDiscoveredRelay(entry); ok {
			t.Fatal("unsafe discovered origin accepted", origin)
		}
	}
	entry.Text = []string{"v=1", "origin=https://a.test", "origin=https://b.test"}
	if _, ok := parseDiscoveredRelay(entry); ok {
		t.Fatal("ambiguous origin accepted")
	}
}
