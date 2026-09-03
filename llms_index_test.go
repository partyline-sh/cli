package main

import "testing"

// The scan cache must: return a cached entry when size is unchanged (no re-scan),
// re-scan when size changes, and prune entries for files not seen in a pass.
func TestScanCache(t *testing.T) {
	scanCache, scanSeen, scanDirty = nil, nil, false
	beginScanPass()

	calls := 0
	scan := func(cwd string) func() scanEntry {
		return func() scanEntry { calls++; return scanEntry{Cwd: cwd} }
	}

	// Miss → scans.
	if got := cachedScan("/a.jsonl", 100, scan("dirA")); got.Cwd != "dirA" || calls != 1 {
		t.Fatalf("first scan: got %+v calls=%d", got, calls)
	}
	// Same size → cache hit, no re-scan.
	if got := cachedScan("/a.jsonl", 100, scan("SHOULD-NOT-RUN")); got.Cwd != "dirA" || calls != 1 {
		t.Fatalf("cache hit expected: got %+v calls=%d", got, calls)
	}
	// Grown file (size changed) → re-scan.
	if got := cachedScan("/a.jsonl", 200, scan("dirA2")); got.Cwd != "dirA2" || calls != 2 {
		t.Fatalf("resize should re-scan: got %+v calls=%d", got, calls)
	}

	// A second pass that doesn't touch /a.jsonl must prune it. Use pruneScanCache
	// (not endScanPass) so the test never writes to the real on-disk index.
	beginScanPass()
	cachedScan("/b.jsonl", 50, scan("dirB"))
	pruneScanCache()
	if _, ok := scanCache["/a.jsonl"]; ok {
		t.Error("/a.jsonl should have been pruned (not seen this pass)")
	}
	if _, ok := scanCache["/b.jsonl"]; !ok {
		t.Error("/b.jsonl should be retained")
	}
}
