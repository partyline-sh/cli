package main

import "testing"

// candidateHandle must be deterministic (a handle re-derives identically when an assignment comes
// back) and distinct per path (two dirs never collide onto one handle).
func TestCandidateHandleStableAndDistinct(t *testing.T) {
	a1 := candidateHandle("/Users/x/dev/foo")
	a2 := candidateHandle("/Users/x/dev/foo")
	b := candidateHandle("/Users/x/dev/bar")
	if a1 != a2 {
		t.Fatalf("handle not deterministic: %q != %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("distinct paths collided onto handle %q", a1)
	}
	if a1 == "" {
		t.Fatal("empty handle")
	}
}

// The reference-not-command invariant: an assignment can bind ONLY a directory the daemon is
// currently advertising. matchHandle resolves a live candidate's handle to its local path and
// rejects everything else — an unknown handle, a stale one, or an empty one.
func TestMatchHandleRejectsUnknown(t *testing.T) {
	cands := []localCandidate{
		{abs: "/Users/x/dev/foo", source: "session"},
		{abs: "/Users/x/dev/bar", source: "registry"},
	}
	if got := matchHandle(cands, candidateHandle("/Users/x/dev/foo")); got != "/Users/x/dev/foo" {
		t.Fatalf("live handle should resolve to its path, got %q", got)
	}
	if got := matchHandle(cands, candidateHandle("/Users/x/dev/not-advertised")); got != "" {
		t.Fatalf("unknown handle must be rejected, got %q", got)
	}
	if got := matchHandle(cands, ""); got != "" {
		t.Fatalf("empty handle must be rejected, got %q", got)
	}
	if got := matchHandle(nil, candidateHandle("/Users/x/dev/foo")); got != "" {
		t.Fatalf("no candidates → nothing resolves, got %q", got)
	}
}

// suggestLabel must always emit a string that passes labelRe (so a web-suggested default is a
// valid label), sanitizing illegal chars and leading punctuation.
func TestSuggestLabelValid(t *testing.T) {
	for _, in := range []string{
		"assetmgmt", "my-repo", "acr-pos", "weird@name!", ".hidden", "___", "café-app",
		"a really long directory name that exceeds forty eight characters total here", "",
	} {
		got := suggestLabel(in)
		if !labelRe.MatchString(got) {
			t.Errorf("suggestLabel(%q) = %q — does not match labelRe", in, got)
		}
	}
}
