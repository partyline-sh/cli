package main

import (
	"path/filepath"
	"strings"
)

// llms_generated.go — recognizing MACHINE-GENERATED session locations.
//
// Agent frameworks (OpenClaw and friends) spawn engine sessions inside per-session workspace
// directories named by ULID/UUID, and open them with injected boilerplate prompts. Scanned
// naively, each of those directories became its own top-level "project" in the switchboard,
// labeled by its ID — a wall of 01KZE9HWBC3PY3K1HP2DQDBBR6 rows that means nothing to a human
// (the screen Matt hit on his first install). The cwd is truthful; the PRESENTATION is the bug.
//
// The rule: an ID-shaped path component is not a project name. Group such sessions by the first
// HUMAN-named ancestor instead (walking past container names like "workspace"), so a framework's
// hundred session dirs collapse into one legible group. Sessions and full paths are untouched —
// the detail pane still shows exactly where each one ran.

// idShapedName reports whether a single path component looks machine-generated:
// a ULID (26 chars, Crockford base32), a UUID, a long hex string, or a `ses_`/`sess_` id.
func idShapedName(name string) bool {
	n := strings.ToLower(name)
	if s, ok := strings.CutPrefix(n, "sess_"); ok {
		n = s
	} else if s, ok := strings.CutPrefix(n, "ses_"); ok {
		n = s
	}
	if len(n) == 26 && isCrockford(n) {
		return true // ULID
	}
	if len(n) == 36 && n[8] == '-' && n[13] == '-' && n[18] == '-' && n[23] == '-' &&
		isHex(strings.ReplaceAll(n, "-", "")) {
		return true // UUID
	}
	if len(n) >= 16 && isHex(n) {
		return true // bare hash
	}
	return false
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}

// isCrockford accepts lowercase Crockford base32 (0-9 a-z minus i, l, o, u).
func isCrockford(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z' && c != 'i' && c != 'l' && c != 'o' && c != 'u':
		default:
			return false
		}
	}
	return len(s) > 0
}

// genericDirNames are components that name a CONTAINER of workspaces, not a project — keep
// walking up past them so the group lands on the tool that owns the container (~/.openclaw).
var genericDirNames = map[string]bool{
	"workspace": true, "workspaces": true, "sessions": true, "session": true,
	"tmp": true, "temp": true, "runs": true, "jobs": true,
}

// collapseGeneratedKey returns the project-grouping key for a cwd: unchanged for human-named
// directories, and the first human-named ancestor for ID-shaped/container ones. Bounded walk —
// a pathological all-generated path just returns what's left.
func collapseGeneratedKey(cwd string) string {
	if cwd == "" {
		return ""
	}
	dir := filepath.Clean(cwd)
	for i := 0; i < 6; i++ {
		base := filepath.Base(dir)
		if base == "/" || base == "." || base == dir {
			break
		}
		plain := strings.ToLower(strings.TrimPrefix(base, "."))
		if !idShapedName(base) && !genericDirNames[plain] {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dir
}
