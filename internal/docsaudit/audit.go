// Epic D — the docs audit, done by EXTRACTION rather than by reading.
//
// WHY THIS EXISTS AT ALL, AND WHY IT IS NOT A THIRD SWEEP. This audit has been done twice, by hand,
// and both times it rotted. It rotted because the only available method was: read the docs, compare
// against memory, fix what you notice. That method cannot prove coverage and cannot be re-run, so
// the docs start drifting the day after it finishes.
//
// The state after two completed sweeps is the argument: llms.txt described a three-feature product
// with no fleet, crank, gates, or threads; `ptln work --help` started a worker instead of printing
// help; `ptln daemon --help` called shipped work "in progress". Every one of those reads fine to a
// human skimming for errors. A set difference sees them immediately.
//
// So: the surface is EXTRACTED from source (Epic S — 485 items and counting), every doc DECLARES
// what it covers, and the two sets are diffed. The gap list is machine-generated, provable, and
// re-runnable on every commit. "Verify against the current version" stops being a human act,
// because the version IS the extracted surface.
//
// TWO DIRECTIONS, and the second is the one that matters most:
//
//   - A surface item nothing claims        → UNDOCUMENTED. The obvious direction.
//   - A claim naming something that is gone → PHANTOM. The direction that catches the failure we
//     actually keep hitting: docs confidently describing a command that no longer exists, or a
//     feature as in-progress years after it shipped. Prose like that reads perfectly.
package docsaudit

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// anchorRe is what an anchor actually looks like: kind:id, no whitespace. Enforced because the
// audit's OWN first run reported eight phantoms out of a task-list file that merely DESCRIBED the
// covers syntax in prose — "covers: [...] using the anchor vocabulary from internal/surfacescan"
// parsed as a claim on a command called "and a new Go test in internal/surfacescan parses".
//
// A prose sentence is not an anchor. Requiring the slug shape costs nothing real (every genuine
// anchor already has it) and removes an entire class of false phantom that would otherwise train
// people to ignore the phantom count — which is the one number in this report that should never be
// ignored.
var anchorRe = regexp.MustCompile(`^[a-z]+:[^\s]+$`)

// Claim is one doc's assertion that it covers a set of surface anchors.
type Claim struct {
	Doc     string   // repo-relative path
	Covers  []string // anchors, e.g. "cli:crank", "api:/api/v1/runs/[id]/approve"
	Ordinal int      // line number of the covers: line, for an actionable error
	// Generated marks a doc written by `make surface-gen`. Such a doc is exempt from staleness
	// stamps: it cannot drift, because the drift check regenerates and compares it.
	Generated bool
}

// Report is the two-directional diff.
type Report struct {
	Undocumented []string  // surface items no doc claims
	Phantom      []Phantom // claims naming something the surface does not have
	Claimed      int
	SurfaceTotal int
}

type Phantom struct {
	Doc    string
	Anchor string
	Line   int
}

// docExts are the file types that may carry a coverage claim. Deliberately narrow: a claim in a
// file nobody reads as documentation is a claim that will not be maintained.
var docExts = map[string]bool{".md": true, ".mdx": true, ".1": true, ".txt": true}

// ParseClaims walks the doc roots and reads `covers:` declarations.
//
// The syntax is one line, in front-matter or in a comment, so it works identically in MDX
// front-matter, a Markdown HTML comment, and a man-page roff comment — three formats that cannot
// share a parser any other way:
//
//	covers: [cli:crank, api:/api/v1/runs, table:run_tasks]
func ParseClaims(root string, dirs []string) ([]Claim, error) {
	var out []Claim
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !docExts[strings.ToLower(filepath.Ext(path))] {
				return nil //nolint:nilerr // an unreadable doc is not an audit failure
			}
			c, perr := parseFile(root, path)
			if perr != nil {
				return perr
			}
			if c != nil {
				out = append(out, *c)
			}
			return nil
		})
		// A missing doc root is not an error: the set of roots is configuration, and failing the
		// whole audit because one directory has not been created yet helps nobody.
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Doc < out[j].Doc })
	return out, nil
}

func parseFile(root, path string) (*Claim, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer f.Close()

	rel, _ := filepath.Rel(root, path)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	generated := false
	var found *Claim
	for sc.Scan() {
		line++
		if strings.Contains(sc.Text(), "GENERATED") && strings.Contains(sc.Text(), "DO NOT EDIT") {
			generated = true
		}
		// Only the head of a file is scanned. A `covers:` line buried in prose is far more likely
		// to be an example of the syntax than a real declaration — and treating an example as a
		// claim would make the audit lie in the direction of "everything is documented".
		if line > 40 {
			break
		}
		t := strings.TrimSpace(sc.Text())
		idx := strings.Index(t, "covers:")
		if idx < 0 {
			continue
		}
		// Take exactly what is BETWEEN the brackets. Trimming suffixes one comment style at a time
		// was silently losing the last anchor in `{/* covers: [...] */}` — the MDX form — because
		// the trailing `*/}` stayed attached to it and the anchor then failed validation and was
		// dropped. Losing a claim makes the audit UNDER-report coverage, which is the safe
		// direction, but silently is never the safe way to do anything.
		rest := t[idx+len("covers:"):]
		open, close := strings.Index(rest, "["), strings.LastIndex(rest, "]")
		if open < 0 || close < open {
			continue
		}
		rest = rest[open+1 : close]
		var anchors []string
		for _, a := range strings.Split(rest, ",") {
			if a = strings.TrimSpace(a); anchorRe.MatchString(a) {
				anchors = append(anchors, a)
			}
		}
		if len(anchors) == 0 {
			continue
		}
		// Record and KEEP SCANNING: the GENERATED marker sits below the front-matter, so returning
		// on the first covers: line meant every generated reference doc was treated as hand-written
		// and demanded a human stamp it can never need. Caught by the count not dropping when the
		// exemption landed.
		if found == nil {
			found = &Claim{Doc: rel, Covers: anchors, Ordinal: line}
		}
	}
	if found != nil {
		found.Generated = generated
	}
	return found, nil
}

// Audit diffs the extracted surface against the claims.
//
// `surface` is the set of anchors the code actually has, in "kind:id" form. Both directions are
// computed in one pass so the report can never say "0 undocumented" while quietly not having
// checked the other way.
func Audit(surface []string, claims []Claim) Report {
	have := make(map[string]bool, len(surface))
	for _, s := range surface {
		have[s] = true
	}
	covered := map[string]bool{}
	rep := Report{SurfaceTotal: len(surface)}

	for _, c := range claims {
		for _, a := range c.Covers {
			if have[a] {
				covered[a] = true
				continue
			}
			// A PHANTOM. The doc asserts something the code does not have — a removed command, a
			// renamed route, a table that never existed. This is the check that catches confident
			// prose about a system that is no longer there.
			rep.Phantom = append(rep.Phantom, Phantom{Doc: c.Doc, Anchor: a, Line: c.Ordinal})
		}
	}
	rep.Claimed = len(covered)

	for _, s := range surface {
		if !covered[s] {
			rep.Undocumented = append(rep.Undocumented, s)
		}
	}
	sort.Strings(rep.Undocumented)
	sort.Slice(rep.Phantom, func(i, j int) bool {
		if rep.Phantom[i].Doc != rep.Phantom[j].Doc {
			return rep.Phantom[i].Doc < rep.Phantom[j].Doc
		}
		return rep.Phantom[i].Anchor < rep.Phantom[j].Anchor
	})
	return rep
}

// Summary is what a human reads first, and it leads with the phantom count because a doc that lies
// is worse than a doc that is missing: the missing one sends you to the source, the lying one sends
// you somewhere confidently wrong.
//
// WHAT "COVERED" MEANS, AND DOES NOT. Covered means A DOC CLAIMS THIS ANCHOR. It does not mean the
// prose is adequate, current, or even really about the thing. A claim is a human assertion the
// machine cannot check — which is precisely why D.3's staleness stamps exist, and why "0
// undocumented" must never be read as "the docs are good". What this number does buy is the
// ratchet: from here, new surface cannot land unclaimed.
func (r Report) Summary() string {
	return fmt.Sprintf(
		"docs audit: %d/%d surface items CLAIMED · %d unclaimed · %d PHANTOM (claims naming code that does not exist)\n"+
			"  (claimed = a doc says it covers this. It is not a judgement about whether the prose is any good.)",
		r.Claimed, r.SurfaceTotal, len(r.Undocumented), len(r.Phantom))
}

// ── what actually REQUIRES documentation ──────────────────────────────────────
//
// The audit's first real run said 81 web routes were undocumented. Inspecting them showed the
// model was wrong rather than the docs: the list included the documentation pages themselves, the
// marketing site, `/login`, `/privacy`, and Next.js intercepting-route artifacts that are literally
// the same page as their target rendered in a modal.
//
// Nobody is going to write a doc for `/login`, and they are right not to. A gap list padded with
// items no one intends to close is a list people stop reading — the exact failure this whole epic
// exists to prevent. So exemptions are declared HERE, each with its reason, rather than being
// quietly dropped: an exemption you can read and argue with is a decision; one buried in a filter
// is a bug waiting to be discovered.
//
// The bar for exemption is "a reader looking for help would never look here", NOT "this is
// tedious to document".

// exemptWeb reports whether a web route is outside the documentation obligation, and why.
func exemptWeb(path string) (string, bool) {
	switch {
	case strings.HasPrefix(path, "/docs"):
		return "is documentation", true
	// Next.js route groups and intercepting routes: `(..)runs/[id]` renders the SAME page as
	// `/runs/[id]` in a modal. Documenting it separately would describe one page twice.
	case strings.Contains(path, "/(") || strings.Contains(path, "[..."):
		return "route-group artifact — same page as its target", true
	case marketingRoutes[path]:
		return "marketing page — covered by the marketing IA, not product docs", true
	case entryRoutes[path]:
		return "auth/legal/entry route — no reader looks for a doc about a login page", true
	}
	return "", false
}

// Marketing pages. Listed explicitly rather than pattern-matched: the day a marketing URL and a
// product URL collide, an explicit list is the one that fails loudly instead of silently exempting
// a real feature.
var marketingRoutes = map[string]bool{
	"/": true, "/pricing": true, "/describe-to-plan": true, "/software-factory": true,
	"/trust-gates": true, "/loop-engineering": true, "/llm-session-manager": true,
	"/tmux-alternative": true, "/share-terminal": true, "/sessions-preview": true,
	"/group-chat-with-humans-and-agents": true, "/shared-context-for-ai-agents": true,
}

// Entry, auth, and legal routes. A user arrives at these by following a link, never by looking
// them up.
var entryRoutes = map[string]bool{
	"/login": true, "/activate": true, "/privacy": true, "/terms": true,
	"/j/[code]": true, "/invite/[token]": true, "/session/[id]": true,
}

// RequiresDoc filters a surface down to the items a doc is actually expected to cover, returning
// the exemptions separately so the report can show its working rather than just a smaller number.
func RequiresDoc(refs []string) (required []string, exempt map[string]string) {
	exempt = map[string]string{}
	for _, r := range refs {
		if path, ok := strings.CutPrefix(r, "web:"); ok {
			if reason, skip := exemptWeb(path); skip {
				exempt[r] = reason
				continue
			}
		}
		required = append(required, r)
	}
	return required, exempt
}

// parseLines is the shared head-of-file scanner: for each doc under dirs, the first line carrying
// `key` yields everything after it. Both the covers claims and the verified_at stamps use it, so
// the two can never disagree about which window of a file counts as declarations.
func parseLines(root string, dirs []string, key string) (map[string]string, error) {
	out := map[string]string{}
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !docExts[strings.ToLower(filepath.Ext(path))] {
				return nil //nolint:nilerr
			}
			f, oerr := os.Open(path)
			if oerr != nil {
				return nil //nolint:nilerr
			}
			defer f.Close()
			rel, _ := filepath.Rel(root, path)
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for line := 0; sc.Scan() && line < 40; line++ {
				t := strings.TrimSpace(sc.Text())
				if i := strings.Index(t, key); i >= 0 {
					out[rel] = strings.TrimSpace(t[i+len(key):])
					break
				}
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return out, nil
}
