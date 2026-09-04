//go:build darwin && tray

package main

import "testing"

// The menu used to read "Daemon: ● connected" whenever `launchctl list` succeeded — i.e. whenever
// launchd had heard of the job. It said connected throughout a fleet-wide outage, while the daemon
// was beating at a hostname that had stopped listening. These pin the rule that green means a
// heartbeat the instance ACCEPTED, and nothing else.

func TestGreenOnlyForAnAcceptedBeat(t *testing.T) {
	d := daemon{Enabled: true, Installed: true, Active: true,
		Link: &link{Connected: true, Detail: "connected to partyline.example.com"}}
	got := daemonLine(d)
	if !contains(got, dotConnected) {
		t.Fatalf("a live connection must show the green dot; got %q", got)
	}
}

// The case the old line got wrong, and the reason this exists: the process is up, the instance is
// not answering. That must NOT read as connected.
func TestRunningButUnreachableIsNotGreen(t *testing.T) {
	d := daemon{Enabled: true, Installed: true, Active: true,
		Link: &link{Connected: false, Detail: "no reply from partyline.example.com for 6m"}}
	got := daemonLine(d)
	if contains(got, dotConnected) {
		t.Fatalf("a daemon that cannot reach its instance must never show green; got %q", got)
	}
	if !contains(got, dotDegraded) {
		t.Fatalf("expected the degraded dot; got %q", got)
	}
	if !contains(got, "no reply") {
		t.Fatalf("the line must say what is wrong, not just that something is; got %q", got)
	}
}

// An older ptln sends no link object. Inferring green from Active is exactly the lie being removed,
// so an unknown state says unknown.
func TestUnknownLinkNeverClaimsConnected(t *testing.T) {
	got := daemonLine(daemon{Enabled: true, Installed: true, Active: true})
	if contains(got, dotConnected) {
		t.Fatalf("an unknown connection state must not render as connected; got %q", got)
	}
	if !contains(got, "unknown") {
		t.Fatalf("it should say the state is unknown; got %q", got)
	}
}

// The tooltip has no menu around it for context, so it must distinguish the three fixes: enrol,
// install, start — and never conflate them.
func TestTooltipNamesTheActualProblem(t *testing.T) {
	cases := []struct {
		name string
		d    daemon
		want string
	}{
		{"not enrolled", daemon{}, "ptln login"},
		{"not installed", daemon{Enabled: true}, "daemon install"},
		{"stopped", daemon{Enabled: true, Installed: true}, "stopped"},
		{"unreachable", daemon{Enabled: true, Installed: true, Active: true,
			Link: &link{Detail: "no reply from x for 6m"}}, "no reply"},
	}
	for _, c := range cases {
		if got := daemonTooltip(c.d); !contains(got, c.want) {
			t.Errorf("%s: got %q, want it to mention %q", c.name, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
