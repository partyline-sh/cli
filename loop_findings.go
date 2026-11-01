package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const loopMinQuote = 12

type loopFinding struct {
	Priority string `json:"priority"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Quote    string `json:"quote"`
	Claim    string `json:"claim"`
	Fix      string `json:"fix"`
	Dropped  string `json:"dropped,omitempty"` // why it was discarded; "" when kept
}

type loopReview struct {
	LimitedUntil     time.Time     `json:"limited_until,omitzero"`
	Replaced         []string      `json:"replaced,omitempty"`
	Engine           string        `json:"engine"`
	Findings         []loopFinding `json:"findings"`
	UnverifiedClaims []string      `json:"unverified_claims"`
	Verdict          string        `json:"verdict"`
	Dropped          []loopFinding `json:"dropped,omitempty"`
	Tokens           loopTokens    `json:"tokens"`
	Error            string        `json:"error,omitempty"`
}

// parseLoopReview reads a reviewer's reply. Models wrap JSON in prose or code fences despite
// being told not to, so the outermost object is taken from wherever it is.
func parseLoopReview(text string) (loopReview, error) {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return loopReview{}, fmt.Errorf("no JSON object in the reply")
	}
	raw := []byte(text[i : j+1])
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return loopReview{}, fmt.Errorf("reply is not the review format: %v", err)
	}
	// A reply missing its findings list is not an approval with nothing found; it is a reply that
	// did not do the review.
	if f, ok := keys["findings"]; !ok || string(f) == "null" {
		return loopReview{}, fmt.Errorf("reply has no findings list")
	}
	// Decode into the reply's own fields only. loopReview also holds partyline's state (error,
	// dropped, tokens), and a reply must not be able to set those about itself.
	var wire struct {
		Findings         []loopFinding `json:"findings"`
		UnverifiedClaims []string      `json:"unverified_claims"`
		Verdict          string        `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return loopReview{}, fmt.Errorf("reply is not the review format: %v", err)
	}
	for i := range wire.Findings {
		wire.Findings[i].Dropped = ""
	}
	r := loopReview{Findings: wire.Findings, UnverifiedClaims: wire.UnverifiedClaims, Verdict: wire.Verdict}
	if r.Verdict != "approve" && r.Verdict != "changes_required" {
		return loopReview{}, fmt.Errorf("verdict %q is not approve or changes_required", r.Verdict)
	}
	return r, nil
}

// verifyFindings keeps the findings whose quote is in the snapshot at the line given (within 3
// lines), and moves the rest to Dropped. A finding that cannot point at real code is either a
// hallucination or text injected through the repository, and neither goes back to the session.
//
// A finding about code the change deletes is checked against the lines the diff removes from
// that file, since the snapshot no longer has them.
func verifyFindings(r *loopReview, dir, diff string) {
	kept := r.Findings[:0]
	for _, f := range r.Findings {
		f.Priority = strings.ToUpper(strings.TrimSpace(f.Priority))
		if why := checkFinding(f, dir); why != "" && !(validPriority(f.Priority) && quotesRemovedLine(f, diff)) {
			f.Dropped = why
			r.Dropped = append(r.Dropped, f)
			continue
		}
		kept = append(kept, f)
	}
	r.Findings = kept
	// The verdict follows the verified findings, not the model's word. A verified P1 means changes
	// are required whatever the reply said. A P1 that could not be verified does not reach the
	// session, but it does not count as an approval either: it fails toward a person looking.
	r.Verdict = "approve"
	for _, f := range r.Dropped {
		if f.Priority == "P1" {
			r.Verdict = "unverified"
		}
	}
	for _, f := range kept {
		if f.Priority == "P1" {
			r.Verdict = "changes_required"
		}
	}
}

func validPriority(p string) bool { return p == "P1" || p == "P2" || p == "P3" }

// quotesRemovedLine reports whether f's quote is a line the diff removes from f's file.
func quotesRemovedLine(f loopFinding, diff string) bool {
	quote := squash(f.Quote)
	if len(quote) < loopMinQuote || strings.TrimSpace(f.Claim) == "" {
		return false
	}
	file, in := filepath.Clean(strings.TrimPrefix(f.File, "./")), false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			in = strings.TrimPrefix(strings.TrimPrefix(line, "--- "), "a/") == file
		case in && strings.HasPrefix(line, "-") && strings.Contains(squash(line[1:]), quote):
			return true
		}
	}
	return false
}

func checkFinding(f loopFinding, dir string) string {
	if !validPriority(f.Priority) {
		return fmt.Sprintf("priority %q is not P1, P2 or P3", f.Priority)
	}
	if strings.TrimSpace(f.Claim) == "" {
		return "no claim"
	}
	// 12 characters with whitespace removed: long enough that "func" or "}" at a plausible line
	// number does not pass as evidence.
	quote := squash(f.Quote)
	if len(quote) < loopMinQuote {
		return fmt.Sprintf("quote is under %d characters", loopMinQuote)
	}
	rel := filepath.Clean(strings.TrimPrefix(f.File, "./"))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Sprintf("file %q is outside the repository", f.File)
	}
	// Resolve symlinks before reading: a link in the repository must not let a finding quote a file
	// outside the snapshot, such as a key in the home directory.
	root, rerr := filepath.EvalSymlinks(dir)
	path, perr := filepath.EvalSymlinks(filepath.Join(dir, rel))
	if rerr != nil || perr != nil {
		return fmt.Sprintf("file %q is not in the snapshot", f.File)
	}
	if inside, _ := filepath.Rel(root, path); inside == ".." || strings.HasPrefix(inside, "../") {
		return fmt.Sprintf("file %q resolves outside the repository", f.File)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("file %q is not in the snapshot", f.File)
	}
	lines := strings.Split(string(b), "\n")
	if f.Line < 1 || f.Line > len(lines) {
		return fmt.Sprintf("line %d is outside %s (%d lines)", f.Line, rel, len(lines))
	}
	lo, hi := max(0, f.Line-1-3), min(len(lines), f.Line+3)
	if !strings.Contains(squash(strings.Join(lines[lo:hi], "\n")), quote) {
		return fmt.Sprintf("quote not found at %s:%d", rel, f.Line)
	}
	return ""
}

// squash removes all whitespace, so a quote survives reindentation and line wrapping.
func squash(s string) string {
	return strings.Join(strings.Fields(s), "")
}
