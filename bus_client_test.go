package main

import (
	"strings"
	"testing"
)

// The hub replaces a connection when a new one arrives with the same identity. Two processes on
// one machine — the memory watcher listening, and `ptln memory add` announcing — must therefore
// not share a hint, or every write silently knocks that machine's listener off the bus.
func TestMachineHintIsUniquePerProcessAndStableWithinIt(t *testing.T) {
	got := machineHint()
	if got != machineHint() {
		t.Fatal("machineHint changed between calls; a reconnect would leave a ghost connection instead of replacing its own")
	}
	if !strings.Contains(got, "-") || len(got) < 3 {
		t.Fatalf("machineHint = %q; want a machine name with a per-process suffix", got)
	}
	// The control plane sanitizes to [A-Za-z0-9._-] and truncates at 40; anything longer or
	// containing other characters loses the part that makes it unique.
	if len(got) > 40 {
		t.Fatalf("machineHint = %q (%d chars); the control plane truncates at 40, which would cut the unique suffix", got, len(got))
	}
	for _, r := range got {
		if !(r == '-' || r == '.' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			t.Fatalf("machineHint = %q contains %q, which the control plane strips", got, r)
		}
	}
}
