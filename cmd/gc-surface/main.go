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
	"path/filepath"
	"sort"

	"partyline.sh/partyline/internal/surfacegen"
	"partyline.sh/partyline/internal/surfacescan"
)

func main() {
	root := flag.String("root", ".", "repository root to scan")
	kind := flag.String("kind", "", "only this kind (api, web, table, env, vocab)")
	summary := flag.Bool("summary", false, "print counts per kind instead of JSON")
	gen := flag.Bool("gen", false, "write every generated artifact")
	check := flag.Bool("check", false, "fail if any generated artifact is out of date")
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
			fmt.Fprintln(os.Stderr, "change the declaration in internal/surface or internal/clispec instead.")
			os.Exit(1)
		}
		fmt.Println("generated artifacts are up to date")
		return
	}

	s, err := surfacescan.Scan(abs)
	if err != nil {
		fail(err)
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
