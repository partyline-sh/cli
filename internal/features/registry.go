// Package features is the registry of which environment variables gate which product feature.
//
// WHY THIS EXISTS. "Is Slack configured on this box?" had no answer you could get without reading
// TypeScript: the knowledge lived in whichever `if (!process.env.X) return` happened to guard the
// code path, and the only restatement of it — deploy/stack/env.example — was hand-maintained and
// therefore already drifting (it lists GITHUB_APP_CLIENT_ID, which nothing reads).
//
// features.json at the repo root is that knowledge, declared once. `ptln server doctor` reads it to
// say configured / not-configured, deploy/stack/env.example is GENERATED from it, and a drift check
// keeps it honest in both directions — see drift.go.
//
// TWO STATES ONLY. A feature is configured or it is not. There is deliberately no "partially
// configured", no "available", no per-var health: every extra state is a state someone has to
// decide the meaning of, and the question being answered here is the operator's simple one.
//
// FAIL LOUDLY. Every parse error is returned, never swallowed. A registry that degrades to "no
// features configured" when the file is malformed reports every feature as off, which is the most
// misleading answer available — it looks like a configuration problem on the box and sends the
// operator to the wrong place entirely.
package features

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Feature is one entry in features.json: what it is called, which variables must ALL be set for it
// to work, and where it is documented.
type Feature struct {
	Key   string   `json:"-"`     // the object key it was declared under
	Label string   `json:"label"` // human name, e.g. "Slack"
	Env   []string `json:"env"`   // every variable required — all must be set
	Docs  string   `json:"docs"`  // doc anchor an operator can follow
}

// Registry is the parsed file, sorted by key so every consumer (doctor output, the generated
// env.example, the drift report) is byte-stable run to run.
type Registry struct {
	Features []Feature
}

// Status is one feature's state. Two states, per the package doc: Configured, or not with the
// missing variable NAMES — never their values.
type Status struct {
	Feature
	Configured bool     `json:"configured"`
	Missing    []string `json:"missing,omitempty"`
}

var (
	keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	varRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// Parse reads features.json. Everything it can detect is an error: a registry that parses into
// something half-formed is worse than one that refuses, because the half-formed one still prints an
// answer.
func Parse(b []byte) (Registry, error) {
	raw := map[string]*Feature{}
	dec := json.NewDecoder(bytes.NewReader(b))
	// A typo'd key ("envs", "vars") would otherwise decode to an empty Env list, and a feature with
	// no required variables reads as permanently CONFIGURED. Reject it at the door.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Registry{}, fmt.Errorf("features.json: %w", err)
	}
	if len(raw) == 0 {
		// An empty registry is indistinguishable from a registry that failed to load, and both
		// render as "nothing is configured". Never accept it.
		return Registry{}, fmt.Errorf("features.json: no features declared")
	}

	reg := Registry{}
	for key, f := range raw {
		if f == nil {
			return Registry{}, fmt.Errorf("features.json: %q is null", key)
		}
		if !keyRe.MatchString(key) {
			return Registry{}, fmt.Errorf("features.json: %q is not a valid key (want lower_snake_case)", key)
		}
		if strings.TrimSpace(f.Label) == "" {
			return Registry{}, fmt.Errorf("features.json: %q has no label", key)
		}
		if strings.TrimSpace(f.Docs) == "" {
			// The doctor's whole job is naming the next step. An entry with nowhere to point is
			// an entry that will print "not configured" and leave the reader stuck.
			return Registry{}, fmt.Errorf("features.json: %q has no docs anchor", key)
		}
		if len(f.Env) == 0 {
			return Registry{}, fmt.Errorf("features.json: %q declares no env vars (it would always read as configured)", key)
		}
		seen := map[string]bool{}
		for _, v := range f.Env {
			if !varRe.MatchString(v) {
				return Registry{}, fmt.Errorf("features.json: %q declares %q, which is not an env var name", key, v)
			}
			if seen[v] {
				return Registry{}, fmt.Errorf("features.json: %q declares %s twice", key, v)
			}
			seen[v] = true
		}
		f.Key = key
		f.Env = append([]string(nil), f.Env...)
		sort.Strings(f.Env)
		reg.Features = append(reg.Features, *f)
	}
	sort.Slice(reg.Features, func(i, j int) bool { return reg.Features[i].Key < reg.Features[j].Key })
	return reg, nil
}

// Path is where the registry lives, relative to the repo root.
const Path = "features.json"

// Load reads and validates the registry from a repository root. Used by the generators and the
// drift check; the CLI uses the EMBEDDED copy instead, so `ptln server doctor` works on a box that
// has no checkout.
func Load(root string) (Registry, error) {
	b, err := os.ReadFile(filepath.Join(root, Path))
	if err != nil {
		return Registry{}, err
	}
	return Parse(b)
}

// Lookup resolves a feature by key.
func (r Registry) Lookup(key string) (Feature, bool) {
	for _, f := range r.Features {
		if f.Key == key {
			return f, true
		}
	}
	return Feature{}, false
}

// Declared reports whether any feature requires this variable.
func (r Registry) Declared(name string) bool {
	for _, f := range r.Features {
		for _, v := range f.Env {
			if v == name {
				return true
			}
		}
	}
	return false
}

// Status evaluates every feature against an environment. look is os.Getenv in production and a map
// in tests; a value that is empty after trimming counts as UNSET, because a trailing newline in a
// secret is this repo's most-repeated outage (see the JWT-secret note in CLAUDE.md) and "set to
// whitespace" has never once meant "configured".
//
// Values are read and immediately discarded. Nothing here returns one.
func (r Registry) Status(look func(string) string) []Status {
	out := make([]Status, 0, len(r.Features))
	for _, f := range r.Features {
		st := Status{Feature: f, Configured: true}
		for _, v := range f.Env {
			if strings.TrimSpace(look(v)) == "" {
				st.Configured = false
				st.Missing = append(st.Missing, v)
			}
		}
		out = append(out, st)
	}
	return out
}
