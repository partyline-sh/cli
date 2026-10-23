package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Session-store scan cache. collectSessions() used to open + read a 1MB prefix of
// EVERY session file (6000+ / ~2GB for a heavy user) on every launcher open — the
// switchboard's 2-3s stall. But the only fields we extract by reading (cwd + the
// first user message = title) are written when a session STARTS and never change.
// So we cache them per file, keyed by size: a dormant file is read once ever; a
// growing (active) file is re-read only when its size changes. Stat-only over all
// files is ~16ms, so subsequent opens are effectively instant.

type scanEntry struct {
	Size  int64  `json:"z"`
	Cwd   string `json:"c,omitempty"`
	Title string `json:"t,omitempty"`
	ID    string `json:"i,omitempty"` // codex/gemini carry an id inside the file body
}

var (
	scanCache map[string]scanEntry
	scanSeen  map[string]bool // paths touched this collect pass (for pruning)
	scanDirty bool
)

func scanIndexPath() string { return filepath.Join(stateDir(), "scan-index.json") }

// beginScanPass loads the cache (once) and resets the per-pass seen-set.
func beginScanPass() {
	if scanCache == nil {
		scanCache = map[string]scanEntry{}
		if b, err := os.ReadFile(scanIndexPath()); err == nil {
			_ = json.Unmarshal(b, &scanCache)
		}
	}
	scanSeen = make(map[string]bool, len(scanCache))
}

// pruneScanCache drops cache entries for files not seen in the just-finished pass
// (sessions whose store was deleted), so the index can't grow unbounded.
func pruneScanCache() {
	if scanSeen == nil {
		return
	}
	for p := range scanCache {
		if !scanSeen[p] {
			delete(scanCache, p)
			scanDirty = true
		}
	}
}

// endScanPass prunes deleted sessions and persists the cache if anything changed.
func endScanPass() {
	pruneScanCache()
	if !scanDirty {
		return
	}
	if b, err := json.Marshal(scanCache); err == nil {
		_ = os.WriteFile(scanIndexPath(), b, 0o600)
	}
	scanDirty = false
}

// cachedScan returns the cached scan for path when its size is unchanged; otherwise
// it runs scan() (the expensive open+read+parse), caches, and returns the result.
func cachedScan(path string, size int64, scan func() scanEntry) scanEntry {
	if scanSeen != nil {
		scanSeen[path] = true
	}
	if e, ok := scanCache[path]; ok && e.Size == size {
		return e
	}
	e := scan()
	e.Size = size
	if scanCache != nil {
		scanCache[path] = e
		scanDirty = true
	}
	return e
}
