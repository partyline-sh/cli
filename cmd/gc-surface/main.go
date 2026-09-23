// Command gc-surface extracts partyline's user-facing surface from source and prints it as JSON.
//
// This is the ground truth every generated artifact and every docs coverage check is diffed
// against. Nothing in the output is hand-maintained: routes come from the App Router's directory
// tree, tables from replaying the migrations, env vars from their call sites, vocabularies from
// internal/surface.
//
//	go run ./cmd/gc-surface                 # JSON to stdout
//	go run ./cmd/gc-surface -summary        # one line per kind, for a human
//	go run ./cmd/gc-surface -kind api       # just one facet
package main

import (
	"flag"
	"fmt"
	"os"
	"partyline.sh/partyline/internal/docsaudit"
	"path/filepath"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/surfacegen"
	"partyline.sh/partyline/internal/surfacescan"
)

func main() {
	root := flag.String("root", ".", "repository root to scan")
	kind := flag.String("kind", "", "only this kind (api, web, table, env, vocab)")
	summary := flag.Bool("summary", false, "print counts per kind instead of JSON")
	gen := flag.Bool("gen", false, "write every generated artifact")
	check := flag.Bool("check", false, "fail if any generated artifact is out of date")
	audit := flag.Bool("audit", false, "diff the extracted surface against docs coverage claims")
	strict := flag.Bool("strict", false, "with -audit: exit non-zero on any unclaimed surface or phantom claim")
	flag.Parse()

	abs, err := filepath.Abs(*root)
	if err != nil {
		fail(err)
	}
	// -gen and -check short-circuit: both are about the ARTIFACTS, not the extraction, and
	// Files() runs its own scan.
	if *gen {
		changed, err := surfacegen.Write(abs)
		if err != nil {
			fail(err)
		}
		for _, c := range changed {
			fmt.Println("wrote", c)
		}
		if len(changed) == 0 {
			fmt.Println("already up to date")
		}
		return
	}
	if *check {
		stale, err := surfacegen.Check(abs)
		if err != nil {
			fail(err)
		}
		if len(stale) > 0 {
			fmt.Fprintln(os.Stderr, "These generated files are out of date or were edited by hand:")
			for _, p := range stale {
				fmt.Fprintln(os.Stderr, "  "+p)
			}
			fmt.Fprintln(os.Stderr, "\nRun `make surface-gen` and commit the result. Do not edit them directly —")
			fmt.Fprintln(os.Stderr, "change the declaration instead: internal/surface or internal/clispec for")
			fmt.Fprintln(os.Stderr, "the lists, and the docs page that owns the prose for a concept in llms-full.txt.")
			os.Exit(1)
		}
		fmt.Println("generated artifacts are up to date")
		return
	}

	s, err := surfacescan.Scan(abs)
	if err != nil {
		fail(err)
	}
	if *audit {
		runAudit(abs, s, *strict)
		return
	}
	if *kind != "" {
		s = surfacescan.Surface{Items: s.OfKind(*kind)}
	}
	if *summary {
		printSummary(s)
		return
	}
	b, err := s.JSON()
	if err != nil {
		fail(err)
	}
	fmt.Println(string(b))
}

func printSummary(s surfacescan.Surface) {
	counts := map[string]int{}
	for _, it := range s.Items {
		counts[it.Kind]++
	}
	var kinds []string
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("%-8s %4d\n", k, counts[k])
	}
	fmt.Printf("%-8s %4d\n", "TOTAL", len(s.Items))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gc-surface:", err)
	os.Exit(1)
}

// runAudit is Epic D's gap check: the extracted surface diffed against what the docs CLAIM to
// cover. It prints the worklist rather than a verdict, because the worklist is the deliverable —
// each line is a bounded task with an executable acceptance criterion (its gap count goes to zero),
// which is the task shape that produces mergeable autonomous work.
//
// It does NOT exit non-zero on undocumented items yet. Failing CI on 400-odd known gaps would make
// the check unpassable from the day it lands, and an unpassable check is one everyone routes
// around — the same reason the lint gate had to become a ratchet before it could become a gate.
// PHANTOMS are different and are called out loudly: a doc naming code that does not exist is
// actively misleading, and there should never be a reason to tolerate one.
func runAudit(root string, s surfacescan.Surface, strict bool) {
	docRoots := []string{"docs", "web/src/app", "web/public"}
	claims, err := docsaudit.ParseClaims(root, docRoots)
	if err != nil {
		fail(err)
	}
	required, exempt := docsaudit.RequiresDoc(s.Refs())
	rep := docsaudit.Audit(required, claims)
	fmt.Println(rep.Summary())
	fmt.Printf("(%d items exempt from the documentation obligation — see docsaudit.RequiresDoc)\n", len(exempt))

	if len(rep.Phantom) > 0 {
		fmt.Println("\nPHANTOM — a doc claims something the code does not have:")
		for _, p := range rep.Phantom {
			fmt.Printf("  %s:%d  %s\n", p.Doc, p.Line, p.Anchor)
		}
	}
	// D.3 — staleness. Separate from the gap check on purpose: "undocumented" and "documented but
	// nobody has re-read it since the code moved" are different problems with different fixes, and
	// folding them into one number would hide the second behind the first.
	sourceOf := map[string]string{}
	for _, it := range s.Items {
		if it.Source != "" {
			sourceOf[it.Ref] = it.Source
		}
	}
	stamps, serr := docsaudit.ParseStamps(root, docRoots)
	if serr != nil {
		fail(serr)
	}
	if stale := docsaudit.CheckStaleness(root, claims, stamps, sourceOf); len(stale) > 0 {
		unstamped := 0
		for _, st := range stale {
			if st.VerifiedAt == "" {
				unstamped++
			}
		}
		fmt.Printf("\nSTALE: %d doc(s) claim code that moved after they were last verified (%d never stamped).\n",
			len(stale)-unstamped, unstamped)
		fmt.Println("  A doc with no `verified_at:` has never been vouched for — which is a different")
		fmt.Println("  fact from one that was verified and has not moved since. Add `verified_at: <sha>`")
		fmt.Println("  after re-reading the prose against the code.")
		for _, st := range stale {
			if len(st.Moved) > 3 {
				fmt.Printf("  %s  (%d anchors moved)\n", st.Doc, len(st.Moved))
			} else {
				fmt.Printf("  %s  %s\n", st.Doc, strings.Join(st.Moved, ", "))
			}
		}
	}

	// Concept blocks carry their own, narrower stamp (`verified:` on the block, against the source
	// files that block was read against) because a page-level stamp would mean vouching for a whole
	// page to change one paragraph — the shape that trains people to stamp without reading.
	if cstale, cerr := surfacegen.StaleConcepts(root); cerr != nil {
		fmt.Printf("\nCONCEPTS: could not be read — %v\n", cerr)
	} else if len(cstale) > 0 {
		fmt.Printf("\nSTALE CONCEPTS: %d block(s) feeding web/public/llms-full.txt were stamped before a\n", len(cstale))
		fmt.Println("  source they cite moved. Re-read the prose, then update its `verified:` sha.")
		for _, c := range cstale {
			fmt.Printf("  %s  [%s] %s\n", c.Doc, c.Slug, strings.Join(c.Moved, ", "))
		}
	}

	byKind := map[string]int{}
	for _, u := range rep.Undocumented {
		if i := strings.Index(u, ":"); i > 0 {
			byKind[u[:i]]++
		}
	}
	fmt.Println("\nUndocumented by kind:")
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("  %-6s %d\n", k, byKind[k])
	}

	// D.6 — THE CLAUSE THAT MAKES THIS THE LAST AUDIT. A command, route, or table merged with no
	// doc claiming it fails the PR, at the moment someone still remembers what it does. Without
	// this the audit is just a third sweep with better tooling: accurate the day it runs, rotting
	// from the day after.
	//
	// Safe to run strict only because the gap is currently zero. A gate that lands already failing
	// is one people route around — see the lint ratchet, which had to become a ratchet BEFORE it
	// could become a gate.
	if strict && (len(rep.Undocumented) > 0 || len(rep.Phantom) > 0) {
		fmt.Fprintln(os.Stderr, "\nNew surface must be claimed by a doc before it merges.")
		fmt.Fprintln(os.Stderr, "Add `covers: [<anchor>]` to the doc that explains it — or, if it genuinely needs no")
		fmt.Fprintln(os.Stderr, "documentation, add it to docsaudit.RequiresDoc with the reason written down.")
		os.Exit(1)
	}
}
