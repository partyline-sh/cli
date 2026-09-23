package main

import (
	"bytes"
	"strings"
	"testing"
)

// A tool advertised with no one-line summary would print as a bare name in `ptln help`; a summary
// for a tool that no longer exists is the failure this whole pass was about.
func TestCgToolIndexIsComplete(t *testing.T) {
	missing, stale := cgToolIndexIsComplete()
	if len(missing) > 0 {
		t.Errorf("advertised tools with no line in cgToolSummary: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("cgToolSummary names tools that are not advertised: %v", stale)
	}
}

// Two tools with one name is ambiguous on the wire: which schema a client keeps is up to the
// client, and the dispatcher can only run one of them. `remember` was advertised twice.
func TestAdvertisedToolNamesAreUnique(t *testing.T) {
	seen := map[string]int{}
	for _, d := range cgToolDefs {
		name, _ := d["name"].(string)
		seen[name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("tool %q advertised %d times", name, n)
		}
	}
}

func TestHelpListsEveryToolOnce(t *testing.T) {
	var b bytes.Buffer
	writeCgToolIndex(&b, "  ")
	for _, d := range cgToolDefs {
		name, _ := d["name"].(string)
		if n := strings.Count(b.String(), " "+name+" "); n != 1 {
			t.Errorf("tool %q appears %d times in the help index, want 1", name, n)
		}
	}
}
