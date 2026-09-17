// Package surfacescan derives partyline's user-facing surface from source.
//
// WHY THIS EXISTS. Docs go stale because keeping them current relies on someone remembering what
// changed. We have run the full docs audit twice (task #163, note #111) by reading the docs and
// comparing against memory, and both times it rotted within weeks — `ptln daemon --help` still
// advertises an MVP that shipped, llms.txt still describes a three-feature product, and
// `ptln work --help` starts a worker instead of printing help.
//
// Reading cannot prove coverage and cannot be re-run. So invert it: extract what the product
// ACTUALLY exposes straight out of the tree, and let the docs declare what they cover. The gap is
// then a set difference — machine-generated, provable, and true on every commit. "Verify the docs
// against the current version" stops being a human act, because the version IS this output.
//
// Everything here is derived. Nothing is hand-maintained, which is the whole point.
package surfacescan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/clispec"
	"partyline.sh/partyline/internal/surface"
)

// Item is one addressable thing the product exposes. Ref is the anchor other tools cite it by —
// deliberately the same slug vocabulary Context Threads already use for entities (decision #135),
// so one index answers both "which docs are stale?" and "which recorded facts are suspect?".
type Item struct {
	Ref    string   `json:"ref"`              // "api:/api/v1/runs", "cli:crank", "table:run_tasks"
	Kind   string   `json:"kind"`             // api | web | table | env | vocab | cli
	Name   string   `json:"name"`             // the bare identifier
	Detail []string `json:"detail,omitempty"` // methods, columns, terms — whatever the kind implies
	Source string   `json:"source,omitempty"` // repo-relative file it was derived from
}

// Surface is the whole extraction, sorted so the output is byte-stable across runs — a generated
// artifact that reorders itself would make every diff unreadable and defeat the drift check.
type Surface struct {
	Items []Item `json:"items"`
}

// Refs returns every item's anchor.
func (s Surface) Refs() []string {
	out := make([]string, 0, len(s.Items))
	for _, it := range s.Items {
		out = append(out, it.Ref)
	}
	return out
}

// Has reports whether ref is part of the extracted surface. This is what turns a doc's `covers:`
// claim naming something that no longer exists into a CI failure rather than plausible prose.
func (s Surface) Has(ref string) bool {
	for _, it := range s.Items {
		if it.Ref == ref {
			return true
		}
	}
	return false
}

// OfKind filters by kind.
func (s Surface) OfKind(kind string) []Item {
	var out []Item
	for _, it := range s.Items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

// Scan walks the repository at root and extracts every facet. Facets that find nothing return
// nothing rather than erroring: a partial extraction is still useful, and a hard failure here
// would block CI on an unrelated refactor. The drift check catches an empty facet separately.
func Scan(root string) (Surface, error) {
	var items []Item
	items = append(items, scanAPIRoutes(root)...)
	items = append(items, scanWebRoutes(root)...)
	items = append(items, scanTables(root)...)
	items = append(items, scanEnv(root)...)
	items = append(items, scanVocabs()...)
	items = append(items, scanCLI()...)
	sort.Slice(items, func(i, j int) bool { return items[i].Ref < items[j].Ref })
	return Surface{Items: items}, nil
}

// scanVocabs projects the S.1 declarations into the same shape as everything else, so a docs page
// can claim `vocab:gate_code` exactly the way it claims `api:/api/v1/runs`.
func scanVocabs() []Item {
	var out []Item
	for _, v := range surface.All() {
		out = append(out, Item{
			Ref:    "vocab:" + v.Name,
			Kind:   "vocab",
			Name:   v.Name,
			Detail: v.Keys(),
			Source: "internal/surface/vocab.go",
		})
	}
	return out
}

// JSON renders the surface for the generators and the docs gap check.
func (s Surface) JSON() ([]byte, error) { return json.MarshalIndent(s, "", "  ") }

// walk visits every file under dir whose name matches want, skipping the directories that would
// otherwise dominate the walk and contribute nothing (node_modules, .git, build output).
func walk(dir string, want func(name string) bool, visit func(path string)) {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is skipped, never fatal
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", ".next", "dist", "out", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if want(info.Name()) {
			visit(path)
		}
		return nil
	})
}

// rel makes a path repo-relative for the Source field, so the output is machine-independent.
func rel(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(path)
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// scanCLI projects the command registry (internal/clispec) into the surface. It is declared
// rather than parsed because argument handling is per-command Go code with no shared shape —
// the registry IS the extraction, which is why S.3 built one.
func scanCLI() []Item {
	var out []Item
	for _, c := range clispec.Commands {
		if c.Hidden {
			continue // spawned by other programs, not part of the documented surface
		}
		out = append(out, Item{
			Ref:    "cli:" + c.Name,
			Kind:   "cli",
			Name:   c.Name,
			Detail: c.AllFlagNames(),
			Source: "internal/clispec/registry.go",
		})
	}
	return out
}
