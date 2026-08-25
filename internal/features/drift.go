package features

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/surfacescan"
)

// Drift is the check that makes this registry stay true, in the same spirit as the docs and surface
// ratchets: the declaration and the code are compared mechanically, and disagreeing is a test
// failure rather than something a reader might notice.
//
// It is a CHECK on top of the extractor in internal/surfacescan/env.go, not a second extractor.
// Four ways the two can disagree, all of them errors:
//
//  1. features.json requires a variable NO code reads. The doctor would then demand a value that
//     changes nothing — the exact staleness deploy/stack/env.example already had
//     (GITHUB_APP_CLIENT_ID, listed for years, read by nothing).
//  2. Code reads a variable that is neither declared nor classified. This is the direction that
//     matters: a new feature gate lands and the doctor silently keeps reporting the old set.
//  3. A classification names a variable nothing reads any more (Compose is exempt — those are read
//     by docker compose and the init scripts, which the extractor does not and should not parse).
//  4. A variable is BOTH declared and classified, so two places disagree about what it is.
//
// Each message names the file to edit, because a drift error an operator cannot act on is just a
// broken build.
func Drift(root string, r Registry) []string {
	s, err := surfacescan.Scan(root)
	if err != nil {
		return []string{fmt.Sprintf("could not scan %s for env readers: %v", root, err)}
	}
	readers := map[string][]string{}
	for _, it := range s.OfKind("env") {
		readers[it.Name] = it.Detail
	}
	return driftAgainst(root, reg{r: r, readers: readers, classified: NonFeatures})
}

// reg bundles the three inputs so the comparison itself can be unit-tested against a small
// hand-built set instead of the whole repository.
type reg struct {
	r          Registry
	readers    map[string][]string
	classified []NonFeature
}

func driftAgainst(root string, in reg) []string {
	r, readers := in.r, in.readers

	var out []string

	// (1) declared but unread
	for _, f := range r.Features {
		for _, v := range f.Env {
			if _, ok := readers[v]; !ok {
				out = append(out, fmt.Sprintf(
					"features.json: feature %q requires %s, but no code reads it — remove it, or fix the name (%s)",
					f.Key, v, "internal/surfacescan/env.go extracts the readers"))
			}
		}
	}

	// (2) read but unaccounted for
	for name := range readers {
		if r.Declared(name) {
			continue
		}
		if _, ok := classifyIn(in.classified, name); ok {
			continue
		}
		cited := readers[name]
		if len(cited) > 3 {
			cited = append(cited[:3:3], "…")
		}
		out = append(out, fmt.Sprintf(
			"%s is read by %s but is neither declared in features.json nor classified in internal/features/classify.go — if it gates a feature, declare it; if not, say why",
			name, strings.Join(cited, ", ")))
	}

	// (3) classified but unread, and (4) classified AND declared
	for _, n := range in.classified {
		if r.Declared(n.Name) {
			out = append(out, fmt.Sprintf(
				"%s is declared in features.json AND classified as %q in internal/features/classify.go — it cannot be both",
				n.Name, n.Class))
			continue
		}
		if n.Class == Compose {
			continue // read by compose/init, invisible to the extractor by design
		}
		if _, ok := readers[n.Name]; !ok {
			out = append(out, fmt.Sprintf(
				"%s is classified in internal/features/classify.go but no code reads it any more — drop the entry",
				n.Name))
		}
	}

	// Every docs anchor must resolve to a file that exists. The doctor's only job when a feature is
	// unconfigured is to name the next step; pointing at a deleted file is worse than pointing
	// nowhere, because the reader spends a turn discovering it.
	for _, f := range r.Features {
		path, _, _ := strings.Cut(f.Docs, "#")
		if path == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			out = append(out, fmt.Sprintf("features.json: feature %q points at %s, which does not exist", f.Key, path))
		}
	}

	sort.Strings(out)
	return out
}
