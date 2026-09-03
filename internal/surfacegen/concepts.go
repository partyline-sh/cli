package surfacegen

// concepts.go — the CONCEPTS section of llms-full.txt, assembled FROM THE DOCS PAGES.
//
// WHY A CONCEPTS SECTION AT ALL. Everything else in llms-full.txt is surface: commands, flags,
// vocabularies, endpoint counts. An agent that knows the surface and not the model gives advice
// that is confident and wrong — it files a plan and reports the work as started, it runs `ptln
// thread attach` and tells the user the project now resolves to that thread, it reads a project
// label off the control plane and treats it as a path. Each of those is a correct call to a real
// command producing a false statement to a human. The surface cannot fix that; only the model can.
//
// WHY IT IS EXTRACTED RATHER THAN WRITTEN HERE. The prose constants at the top of llms.go are the
// exception this file exists to stop spreading: hand-written text in a generator is a second place
// an explanation lives, with its own decay rate, and the docs pages are already audited (`make
// docs-audit`) and stamped. So a concept is written ONCE, in the docs page that owns it, wrapped in
// a marker; this file only collects the marked blocks and orders them.
//
// The consequence is the point: fixing a doc fixes the artifact, and there is no way to fix the
// artifact without fixing a doc — a hand edit to the generated section is reverted by the next
// `make surface-gen` and fails `make surface-check` in the meantime.
//
// WHAT GENERATION REFUSES TO DO. Generation FAILS, rather than emitting a partial section, when:
//
//   - a required concept has no block anywhere in the docs (a concept silently vanishing from
//     llms-full.txt because someone rewrote a page is exactly the drift this replaces),
//   - a block carries no `verified:` sha — nobody has vouched for that prose against the code, and
//     laundering unverified text through a generator gives it authority it has not earned,
//   - a block names a source file that does not exist, or has no body.
//
// Staleness — the source moved after the stamp — is NOT a generation failure. It is reported by
// `make docs-audit` alongside the page-level staleness it already reports. A source edit making an
// unrelated build red would get this gate switched off within a week, and a gate that is off
// catches nothing.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/docsaudit"
)

// docsRoot is where the marked prose lives. One directory, deliberately: a concept sourced from
// somewhere that is not a published, audited docs page is a concept nobody maintains.
const docsRoot = "web/src/app/(marketing)/docs"

// requiredConcepts is the section's contract AND its order — the order an agent should meet them
// in, not alphabetical. Each entry must be found in the docs; a missing one fails generation.
//
// The list is short on purpose. This is not "every idea in partyline"; it is the set where knowing
// the surface without the model produces a confidently wrong answer.
var requiredConcepts = []string{
	"context-thread",
	"thread-project-link",
	"filing-is-not-starting",
	"run-mode",
	"reference-not-command",
	"crank-or-party",
}

// concept is one marked block of prose lifted out of a docs page.
type concept struct {
	Slug     string
	Title    string
	Doc      string   // repo-relative path of the page that owns the explanation
	Sources  []string // repo-relative code files the prose was read against
	Verified string   // the sha a human read it at
	Body     []string
}

// conceptBlockSyntax documents the marker, for the person who has to add one:
//
//	{/* concept: run-mode
//	    title: Filing is not starting
//	    sources: daemon.go, cg_mcp.go
//	    verified: 55ccde12 */}
//
//	…prose, as many paragraphs as it takes…
//
//	{/* endconcept */}
//
// The stamp key is `verified:` and NOT `verified_at:` on purpose: `verified_at:` is the PAGE-level
// stamp, and internal/docsaudit reads it by substring within the first 40 lines of a file. A block
// stamp using that spelling near the top of a page would register as a stamp for the whole page —
// a human vouching for six paragraphs would silently become a human vouching for six sections.
const conceptBlockSyntax = "{/* concept: <slug> … verified: <sha> */} … {/* endconcept */}"

// loadConcepts reads every marked block under docsRoot and returns them in requiredConcepts order.
func loadConcepts(root string) ([]concept, error) {
	base := filepath.Join(root, filepath.FromSlash(docsRoot))
	var found []concept
	err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".mdx") {
			return nil //nolint:nilerr // an unreadable page is caught by the missing-concept check
		}
		rel, _ := filepath.Rel(root, path)
		cs, perr := parseConcepts(filepath.ToSlash(rel), path)
		if perr != nil {
			return perr
		}
		found = append(found, cs...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	byslug := map[string]concept{}
	for _, c := range found {
		if prev, dup := byslug[c.Slug]; dup {
			return nil, fmt.Errorf("concept %q is defined twice: %s and %s — one place per explanation",
				c.Slug, prev.Doc, c.Doc)
		}
		byslug[c.Slug] = c
	}

	var out []concept
	for _, slug := range requiredConcepts {
		c, ok := byslug[slug]
		if !ok {
			return nil, fmt.Errorf("no docs page defines the concept %q.\n"+
				"llms-full.txt must cover it, and it may only be written in a docs page under %s — "+
				"add %s there rather than writing it into the generated file",
				slug, docsRoot, conceptBlockSyntax)
		}
		if err := validateConcept(root, c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	// Anything marked but not required is a page-owned concept nobody asked for in the artifact.
	// Report it rather than dropping it silently: a block whose slug has a typo would otherwise
	// look like it landed while the required-concept check complained about something else.
	var extra []string
	for slug := range byslug {
		if !contains(requiredConcepts, slug) {
			extra = append(extra, slug)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return nil, fmt.Errorf("concept block(s) %s are marked in the docs but not in requiredConcepts — "+
			"add them to internal/surfacegen/concepts.go or fix the slug", strings.Join(extra, ", "))
	}
	return out, nil
}

func validateConcept(root string, c concept) error {
	if c.Title == "" {
		return fmt.Errorf("%s: concept %q has no `title:`", c.Doc, c.Slug)
	}
	if c.Verified == "" {
		return fmt.Errorf("%s: concept %q has no `verified: <sha>`.\n"+
			"An unstamped explanation is one nobody has read against the code, and generating from it "+
			"gives stale prose the authority of a machine-produced file. Read it against %s and stamp it",
			c.Doc, c.Slug, strings.Join(c.Sources, ", "))
	}
	if len(c.Sources) == 0 {
		return fmt.Errorf("%s: concept %q has no `sources:` — a stamp with nothing to be stale against "+
			"cannot be checked, so it is worse than no stamp", c.Doc, c.Slug)
	}
	for _, src := range c.Sources {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(src))); err != nil {
			return fmt.Errorf("%s: concept %q names a source that does not exist: %s", c.Doc, c.Slug, src)
		}
	}
	if len(c.Body) == 0 {
		return fmt.Errorf("%s: concept %q has no prose between its marker and `endconcept`", c.Doc, c.Slug)
	}
	return nil
}

// parseConcepts scans one page. Deliberately line-based rather than an MDX parse: the marker has to
// work in whatever a page happens to contain, and a comment is the one construct that means the
// same thing in MDX, Markdown and a man page.
func parseConcepts(rel, path string) ([]concept, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer f.Close()

	var out []concept
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	var cur *concept
	inHeader := false
	line := 0
	for sc.Scan() {
		line++
		t := strings.TrimSpace(sc.Text())
		switch {
		case cur == nil:
			slug, ok := headerStart(t)
			if !ok {
				continue
			}
			cur = &concept{Slug: slug, Doc: rel}
			inHeader = !strings.Contains(t, "*/")
			readHeaderFields(cur, t)
		case inHeader:
			readHeaderFields(cur, t)
			if strings.Contains(t, "*/") {
				inHeader = false
			}
		case strings.Contains(t, "endconcept"):
			cur.Body = trimBlankEdges(cur.Body)
			out = append(out, *cur)
			cur = nil
		default:
			cur.Body = append(cur.Body, sc.Text())
		}
	}
	if cur != nil {
		return nil, fmt.Errorf("%s: concept %q is never closed — add `{/* endconcept */}`", rel, cur.Slug)
	}
	return out, sc.Err()
}

// headerStart recognises the opening line and returns the slug.
func headerStart(t string) (string, bool) {
	i := strings.Index(t, "concept:")
	// "endconcept" contains "concept" but not "concept:", so no special case is needed; a prose
	// line merely mentioning `concept:` inside a comment marker is the only false positive, and the
	// marker must be at the start of the line for that reason.
	if i < 0 || !strings.HasPrefix(t, "{/*") {
		return "", false
	}
	rest := strings.TrimSpace(t[i+len("concept:"):])
	slug := strings.Fields(strings.TrimSuffix(strings.TrimSpace(rest), "*/}"))
	if len(slug) == 0 {
		return "", false
	}
	return strings.TrimSpace(slug[0]), true
}

func readHeaderFields(c *concept, t string) {
	t = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "}")), "*/")
	if v, ok := headerField(t, "title:"); ok {
		c.Title = v
	}
	if v, ok := headerField(t, "verified:"); ok {
		c.Verified = strings.Fields(v)[0]
	}
	if v, ok := headerField(t, "sources:"); ok {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				c.Sources = append(c.Sources, s)
			}
		}
	}
}

func headerField(t, key string) (string, bool) {
	i := strings.Index(t, key)
	if i < 0 {
		return "", false
	}
	v := strings.TrimSpace(t[i+len(key):])
	if v == "" {
		return "", false
	}
	return v, true
}

func trimBlankEdges(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// genConcepts renders the section. It states where each explanation came from, so a model that
// finds this text wrong knows which file to fix — and so a person reading the artifact can see that
// nothing here was written in the generator.
func genConcepts(cs []concept) string {
	var b strings.Builder
	b.WriteString("## Concepts — the model behind the surface\n\n")
	b.WriteString("The commands and endpoints below this section tell you what partyline can DO. This section\n")
	b.WriteString("is what it MEANS, and it is the part an agent gets wrong: every explanation here is one\n")
	b.WriteString("where knowing the command without the model produces a confident, wrong statement to a\n")
	b.WriteString("human. Each is extracted verbatim from the documentation page that owns it — the citation\n")
	b.WriteString("is the file to correct if any of it is wrong.\n\n")
	for _, c := range cs {
		fmt.Fprintf(&b, "### %s\n\n", c.Title)
		b.WriteString(strings.Join(c.Body, "\n"))
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "_Source: %s (verified at %s against %s)_\n\n", c.Doc, c.Verified, strings.Join(c.Sources, ", "))
	}
	return b.String()
}

// ConceptStale is one concept block whose prose was stamped before a file it was read against moved.
type ConceptStale struct {
	Slug     string
	Doc      string
	Verified string
	Moved    []string // sources touched after the stamp
}

// StaleConcepts reports concept blocks whose stamp predates a change to a source they cite.
//
// This is REPORTED (`make docs-audit`), not enforced by generation. The difference is deliberate:
// generation refuses prose nobody has ever vouched for, because that is a one-time cost paid by
// reading the prose once; it does not refuse prose that has merely aged, because that would turn
// every edit to daemon.go into a red build on an unrelated PR, and a gate people route around
// catches nothing. Staleness belongs next to the page-level staleness the audit already prints.
func StaleConcepts(root string) ([]ConceptStale, error) {
	cs, err := loadConcepts(root)
	if err != nil {
		return nil, err
	}
	lastTouched := map[string]string{}
	var out []ConceptStale
	for _, c := range cs {
		var moved []string
		for _, src := range c.Sources {
			last, ok := lastTouched[src]
			if !ok {
				last = docsaudit.LastTouched(root, src)
				lastTouched[src] = last
			}
			if last != "" && docsaudit.IsAncestor(root, c.Verified, last) && !docsaudit.SameCommit(root, c.Verified, last) {
				moved = append(moved, src)
			}
		}
		if len(moved) > 0 {
			sort.Strings(moved)
			out = append(out, ConceptStale{Slug: c.Slug, Doc: c.Doc, Verified: c.Verified, Moved: moved})
		}
	}
	return out, nil
}
