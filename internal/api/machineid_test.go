package api

import "testing"

// The two properties that make this identity safe to send AND useful for dedupe.
//
// If it is not stable, re-registration mints a new row again and runs keep getting stranded — the
// exact bug this was written to fix, silently reintroduced. If it is not hashed, a raw hardware
// identifier ends up in the database, which is a liability with no matching benefit.
func TestMachineIDIsStableAndNeverSendsTheRawIdentifier(t *testing.T) {
	first, second := MachineID(), MachineID()
	if first != second {
		t.Fatalf("machine id is not stable across calls: %q vs %q", first, second)
	}

	raw := rawMachineIdentifier()
	if raw == "" {
		// A platform with no stable id must yield EMPTY, never a fabricated value: the server reads
		// empty as "cannot dedupe" and falls back to insert. Inventing an id that drifts would be
		// worse than not deduping at all.
		if first != "" {
			t.Errorf("no platform identifier available, but MachineID returned %q — invented an identity", first)
		}
		t.Skip("no platform machine id on this host")
	}

	if len(first) != 64 {
		t.Errorf("len = %d, want a 64-char sha256 hex digest", len(first))
	}
	// The whole point: the digest must not be, contain, or trivially reveal the hardware id.
	if first == raw {
		t.Fatal("MachineID returned the RAW hardware identifier")
	}
	if len(raw) > 6 && containsFold(first, raw) {
		t.Fatal("the raw hardware identifier is embedded in the value we send")
	}
}

func containsFold(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if equalFold(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
