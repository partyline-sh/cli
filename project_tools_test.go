package main

import (
	"strings"
	"testing"
)

// The grants editor must be idempotent and order-stable: adds dedupe, revokes drop exactly the
// named entry, and everything else survives untouched.
func TestEditList(t *testing.T) {
	cur := []string{"gh *", "git log *"}
	out := editList(cur, []string{"gh *", "npm test"}, []string{"git log *"})
	if got := strings.Join(out, ","); got != "gh *,npm test" {
		t.Fatalf("editList = %q", got)
	}
	if got := editList(nil, nil, nil); len(got) != 0 {
		t.Fatalf("empty in, empty out: %v", got)
	}
	if got := editList([]string{"a"}, []string{" a "}, nil); len(got) != 1 {
		t.Fatalf("trimmed duplicates must collapse: %v", got)
	}
}
