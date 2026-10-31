package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// The relay updates itself after a partyline update only when this machine deployed it, a token
// is saved, and the relay does not run a newer partyline; one try per build per hour, and a
// failure is reported where the person looks.
func TestRelayAutoUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prevVersion := version
	version = "1.0.0" // a released build; "dev" counts as older than any relay
	defer func() { version = prevVersion }()
	calls := 0
	fail := false
	prev := runRelayDeploy
	runRelayDeploy = func(context.Context, *os.File) error {
		calls++
		if fail {
			return errors.New("npm not found")
		}
		return nil
	}
	defer func() { runRelayDeploy = prev }()
	base := relayConfig{URL: "https://ptln-relay.example.workers.dev", RPID: "ptln-relay.example.workers.dev", MachineID: strings.Repeat("11", 16)}
	save := func(c relayConfig) {
		if err := saveRelay(c, strings.Repeat("ab", 32)); err != nil {
			t.Fatal(err)
		}
	}
	// Joined machine (never deployed it), and no token: nothing.
	save(base)
	relayAutoUpdate(context.Background())
	deployed := base
	deployed.Sources, deployed.Version = "older-sources", "0.0.1"
	save(deployed)
	relayAutoUpdate(context.Background())
	if calls != 0 {
		t.Fatalf("deployed without a token or from a joined machine: %d", calls)
	}
	if err := saveRelayAPIToken("cf-token"); err != nil {
		t.Fatal(err)
	}
	// A relay running a newer partyline is never downgraded.
	newer := deployed
	newer.Version = "999.0.0"
	save(newer)
	relayAutoUpdate(context.Background())
	if calls != 0 || !strings.Contains(relayAutoLine(newer), "newer partyline") {
		t.Fatalf("downgraded a newer relay: %d %q", calls, relayAutoLine(newer))
	}
	// Eligible: deploys once, and not again within the hour; a failure is reported.
	save(deployed)
	fail = true
	relayAutoUpdate(context.Background())
	relayAutoUpdate(context.Background())
	if calls != 1 {
		t.Fatalf("deploys: %d, want 1", calls)
	}
	if line := relayAutoLine(deployed); !strings.Contains(line, "failed") || !strings.Contains(line, "auto-update.log") {
		t.Errorf("a failed update is not reported: %q", line)
	}
	// Current: nothing to do.
	current := deployed
	current.Sources = relaySourcesDigest()
	save(current)
	relayAutoUpdate(context.Background())
	if calls != 1 {
		t.Fatal("deployed a current relay")
	}
}
