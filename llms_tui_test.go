package main

import "testing"

// rowKinds flattens m.rows to a comparable list: "H:<proj>" for a header, "S:<id>" for a session.
func rowKinds(m *aiMenu) []string {
	out := make([]string, 0, len(m.rows))
	for _, r := range m.rows {
		if r.header() {
			out = append(out, "H:"+r.proj)
		} else {
			out = append(out, "S:"+m.view[r.sessIdx].ID)
		}
	}
	return out
}

func eqRowKinds(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBuildRowsTree(t *testing.T) {
	// Two projects: /a has two sessions, /b has one. (Project order follows view order.)
	mk := func() *aiMenu {
		return &aiMenu{view: []aiSession{
			{ID: "s1", Cwd: "/a"}, {ID: "s2", Cwd: "/a"}, {ID: "s3", Cwd: "/b"},
		}}
	}

	// (a) collapsed project shows only its header; (b) expanded shows header+children;
	// (c) project order = view order; (d) single-session project /b still gets a header.
	m := mk()
	m.collapsed = map[string]bool{"/a": true, "/b": false}
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "H:/b", "S:s3"}; !eqRowKinds(got, want) {
		t.Fatalf("collapsed /a + expanded /b: got %v want %v", got, want)
	}

	// default (no overrides, nothing live) → everything collapsed
	m = mk()
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "H:/b"}; !eqRowKinds(got, want) {
		t.Fatalf("default collapsed: got %v want %v", got, want)
	}

	// a live session auto-expands its project (active work shows on open)
	m = mk()
	m.live = map[string]bool{"s1": true}
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "S:s1", "S:s2", "H:/b"}; !eqRowKinds(got, want) {
		t.Fatalf("auto-expand live: got %v want %v", got, want)
	}

	// a search query expands everything regardless of collapse state
	m = mk()
	m.collapsed = map[string]bool{"/a": true, "/b": true}
	m.query = "x"
	m.buildRows()
	if got, want := rowKinds(m), []string{"H:/a", "S:s1", "S:s2", "H:/b", "S:s3"}; !eqRowKinds(got, want) {
		t.Fatalf("query expands all: got %v want %v", got, want)
	}

	// (e) header session counts are correct
	m = mk()
	m.collapsed = map[string]bool{"/a": false, "/b": false}
	m.buildRows()
	counts := map[string]int{}
	for _, r := range m.rows {
		if r.header() {
			counts[r.proj] = r.count
		}
	}
	if counts["/a"] != 2 || counts["/b"] != 1 {
		t.Fatalf("header counts: /a=%d (want 2) /b=%d (want 1)", counts["/a"], counts["/b"])
	}
}
