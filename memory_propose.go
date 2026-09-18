package main

// `ptln memory propose` — the sink for backfill.
//
// The sources worth mining are already reachable by an agent: a project tracker over MCP, a
// session-memory plugin, git history, a chat export. Writing a connector for each one inside
// partyline means chasing four APIs and taking a hard dependency on tools that are not ours.
//
// So the split is: the AGENT gathers and distills, which needs judgement; partyline takes what
// it produces and does the parts that must be deterministic and auditable — dedupe, provenance,
// ranking, and a branch a human reviews. Any source an agent can reach becomes a backfill source
// with no code here.
//
// WHY PROPOSED FACTS ARE MARKED AND RANKED LOWER. A fact inferred from a two-year-old ticket is
// weaker evidence than one a person wrote this morning, and an LLM's summary of a session is two
// lossy steps from the work it describes. If the two look identical in a brief, the memory's
// credibility drops to the level of its weakest entry — and a brief people stop trusting is
// worse than no brief. So a proposed fact says where it came from, and sorts under authored
// facts rather than competing with them.

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// proposedBy is the author recorded for a fact nobody typed by hand. It reads as what it is in
// every place a fact's author is shown.
const proposedBy = "harvest"

// proposalBranch is where proposals land. Review is a pull request on the memory repo: it is a
// git repo, so the review tool already exists and everyone already knows it. Rejecting a fact is
// deleting a file.
const proposalBranch = "memory/proposed"

// sourceRef is one citation — "odoo:ACR-1412", "commit:4a50eb9", "claude-mem:53485".
//
// Citations are what make review fast enough to happen. Without them a reviewer judges "is this
// true?", which is slow and needs the reviewer to already know. With them the judgement is "does
// this match what it cites?", which is seconds. Review throughput is the real limit on any
// backfill, so this is not bookkeeping.
func validSourceRef(s string) bool {
	i := strings.IndexByte(s, ':')
	return i > 0 && i < len(s)-1 && !strings.ContainsAny(s, " \t\n")
}

// dedupeAgainst reports whether an existing fact already says this. Exact-body matching only:
// guessing at paraphrase would silently drop facts that differ in ways that matter, and a
// near-duplicate surviving to review costs one keystroke while a wrongly-dropped fact is gone
// with nobody knowing.
func dedupeAgainst(existing []fact, body string) (fact, bool) {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	want := norm(body)
	for _, f := range existing {
		if norm(f.Body) == want {
			return f, true
		}
	}
	return fact{}, false
}

// memoryProposeMain is `ptln memory propose [--from <src>]... <kind> "<body>"`.
func memoryProposeMain(args []string) {
	var sources []string
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--from" && i+1 < len(args) {
			sources, i = append(sources, args[i+1]), i+1
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) < 2 {
		fatal(fmt.Errorf("usage: ptln memory propose --from <source> <kind> \"<what was learned>\"\n" +
			"  a source is <system>:<id>, e.g. commit:4a50eb9, odoo:ACR-1412"))
	}
	kind, body := strings.ToLower(rest[0]), strings.Join(rest[1:], " ")
	if !validFactKind(kind) {
		fatal(fmt.Errorf("kind must be one of %s", strings.Join(factKinds, ", ")))
	}
	// A proposal with no citation is an assertion, and an assertion nobody can check is the thing
	// this whole mechanism exists to avoid. Refuse it rather than record it weakly.
	if len(sources) == 0 {
		fatal(fmt.Errorf("a proposed fact needs at least one --from <system>:<id> — without a citation " +
			"a reviewer has to already know the answer, and review is what makes backfill safe"))
	}
	for _, s := range sources {
		if !validSourceRef(s) {
			fatal(fmt.Errorf("source %q is not <system>:<id> (e.g. commit:4a50eb9, odoo:ACR-1412)", s))
		}
	}

	cwd, _ := os.Getwd()
	ws, ok := projectForDir(cwd)
	if !ok {
		fatal(fmt.Errorf("this repo is not in a project — `ptln project add-repo <label>` first"))
	}
	existing, err := readFacts(ws.Dir, true)
	if err != nil {
		fatal(err)
	}
	if dup, found := dedupeAgainst(existing, body); found {
		fmt.Printf("already known — %s (%s)\nnothing recorded\n", dup.ID, dup.By)
		return
	}

	f := fact{
		Kind:    kind,
		Repo:    ws.repoNameFor(cwd),
		By:      proposedBy,
		At:      time.Now(),
		Sources: sources,
		Body:    body,
	}
	path, err := writeFact(ws.Dir, f)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("proposed  %s\n  %s\n  cites: %s\n", f.ID, clipVis(oneLine(body), 66), strings.Join(sources, ", "))
	fmt.Printf("\nreview it with the others, then share:  ptln memory sync\n")
	_ = path
}
