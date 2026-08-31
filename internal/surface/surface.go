// Package surface is the single declaration of every closed vocabulary partyline uses on a
// boundary — run statuses, presets, merge policies, gate outcomes, and the rest.
//
// WHY THIS EXISTS. Each of these lists used to be written down two or three times: once in Go,
// once in TypeScript, once as a Postgres CHECK constraint, and often again in a doc. The copies
// drift, and the drift is only ever found in production. Run presets are the proven case — the
// allowlist in web/src/lib/api/runs.ts and the runs_preset_check constraint are the same list,
// and adding a preset to one but not the other makes the insert fail with 23514 and the endpoint
// 500 (recorded as constraint #195, i.e. after it happened).
//
// So: declare each vocabulary exactly once, HERE, with its documentation attached to the term
// rather than filed somewhere else. Everything downstream — TypeScript unions, SQL constraints,
// the docs reference tables, UI copy keys — is generated from these declarations by cmd/gc-surface
// and checked for drift in CI. A term without a doc string is a build-breaking test failure, which
// is what stops the reference rotting the way llms.txt did.
//
// This package MUST NOT import anything from partyline outside the standard library. It is the
// bottom of the dependency graph; the generator, the daemon, and the CLI all read from it.
package surface

import (
	"fmt"
	"sort"
	"strings"
)

// Class is the retry disposition of a failure. It is the field that lets the system tell "the
// provider throttled us" apart from "your code is wrong" — a distinction the gate could not make
// while its only output was prose, and the reason a rate-limited run used to surface to a human
// as an approval request with nothing to approve.
type Class string

const (
	// ClassNone marks an outcome that is not a failure at all.
	ClassNone Class = "none"
	// ClassTransient marks a failure worth retrying without a human: the work was never judged.
	ClassTransient Class = "transient"
	// ClassHard marks a failure a retry cannot fix. A human decides what happens next.
	ClassHard Class = "hard"
)

// Classes is the closed set, in the order the generated artifacts should present it.
var Classes = []Class{ClassNone, ClassTransient, ClassHard}

// Term is one member of a vocabulary. Doc is not optional: it is the source text for the generated
// reference, so a term without one would generate a documentation page with a hole in it.
type Term struct {
	Key string
	Doc string
	// Class is meaningful only for failure vocabularies (gate codes). Elsewhere it is empty.
	Class Class
}

// Vocab is one closed set plus the places it has to agree with.
type Vocab struct {
	// Name is the identifier the generators use: the TS type name, the docs anchor, the SQL
	// constraint stem. Lowercase snake_case.
	Name string
	// Doc explains what the vocabulary is for. Becomes the reference section's preamble.
	Doc string
	// Column is the "table.column" this vocabulary constrains, or "" when it has no DB home.
	// Present ⇒ the generator emits a CHECK constraint for it.
	Column string
	// Classed marks a vocabulary whose terms must each carry a Class.
	Classed bool
	Terms   []Term
}

// Keys returns the vocabulary's terms as plain strings, declaration order preserved.
func (v Vocab) Keys() []string {
	out := make([]string, 0, len(v.Terms))
	for _, t := range v.Terms {
		out = append(out, t.Key)
	}
	return out
}

// Has reports whether key is a member. This is the runtime validator every boundary should use
// instead of an inline slice literal.
func (v Vocab) Has(key string) bool {
	for _, t := range v.Terms {
		if t.Key == key {
			return true
		}
	}
	return false
}

// ClassOf returns the retry disposition declared for key. Unknown keys are ClassHard: an outcome
// we do not recognise is not one we may silently retry.
func (v Vocab) ClassOf(key string) Class {
	for _, t := range v.Terms {
		if t.Key == key {
			return t.Class
		}
	}
	return ClassHard
}

// All is every declared vocabulary. The generators walk this; adding a Vocab here is the only
// step needed to give it a TypeScript union, a docs table, and (with Column set) a constraint.
func All() []Vocab {
	return []Vocab{
		RunStatus,
		TaskStatus,
		PauseReason,
		Preset,
		MergePolicy,
		Engine,
		GateVerdict,
		GateCode,
	}
}

// Lookup returns a vocabulary by name.
func Lookup(name string) (Vocab, bool) {
	for _, v := range All() {
		if v.Name == name {
			return v, true
		}
	}
	return Vocab{}, false
}

// Validate enforces the invariants the generators depend on. It is called by the package test and
// by the generator before it writes anything — a malformed declaration must fail loudly at build
// time, never produce a half-correct artifact that looks authoritative.
func Validate() error {
	var problems []string
	seenVocab := map[string]bool{}
	for _, v := range All() {
		if v.Name == "" || v.Name != strings.ToLower(v.Name) {
			problems = append(problems, fmt.Sprintf("vocabulary %q: name must be lowercase and non-empty", v.Name))
		}
		if seenVocab[v.Name] {
			problems = append(problems, fmt.Sprintf("vocabulary %q: declared twice", v.Name))
		}
		seenVocab[v.Name] = true
		if strings.TrimSpace(v.Doc) == "" {
			problems = append(problems, fmt.Sprintf("vocabulary %q: needs a Doc", v.Name))
		}
		if len(v.Terms) == 0 {
			problems = append(problems, fmt.Sprintf("vocabulary %q: has no terms", v.Name))
		}
		if v.Column != "" && len(strings.Split(v.Column, ".")) != 2 {
			problems = append(problems, fmt.Sprintf("vocabulary %q: Column %q must be table.column", v.Name, v.Column))
		}
		problems = append(problems, validateTerms(v)...)
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("surface declarations invalid:\n  %s", strings.Join(problems, "\n  "))
}

func validateTerms(v Vocab) []string {
	var problems []string
	seen := map[string]bool{}
	for _, t := range v.Terms {
		switch {
		case t.Key == "":
			problems = append(problems, fmt.Sprintf("vocabulary %q: a term has an empty Key", v.Name))
			continue
		case t.Key != strings.ToLower(t.Key), strings.ContainsAny(t.Key, " \t\n"):
			problems = append(problems, fmt.Sprintf("%s.%s: keys are lowercase and whitespace-free", v.Name, t.Key))
		}
		if seen[t.Key] {
			problems = append(problems, fmt.Sprintf("%s.%s: declared twice", v.Name, t.Key))
		}
		seen[t.Key] = true
		// The rule that keeps the generated reference honest: no term ships undocumented.
		if strings.TrimSpace(t.Doc) == "" {
			problems = append(problems, fmt.Sprintf("%s.%s: needs a Doc — it is the generated reference text", v.Name, t.Key))
		}
		if v.Classed && t.Class == "" {
			problems = append(problems, fmt.Sprintf("%s.%s: needs a Class (none|transient|hard)", v.Name, t.Key))
		}
		if !v.Classed && t.Class != "" {
			problems = append(problems, fmt.Sprintf("%s.%s: has a Class but %s is not a classed vocabulary", v.Name, t.Key, v.Name))
		}
		if t.Class != "" && !validClass(t.Class) {
			problems = append(problems, fmt.Sprintf("%s.%s: unknown Class %q", v.Name, t.Key, t.Class))
		}
	}
	return problems
}

func validClass(c Class) bool {
	for _, known := range Classes {
		if c == known {
			return true
		}
	}
	return false
}
