package docsaudit

// D.3 — staleness, for the half of documentation a generator can never write.
//
// D.2 answers "is this thing documented at all". It cannot answer the question that actually rots
// docs, which is "the code moved — is the prose still true?". A `covers:` claim is a human
// assertion, and a human assertion made in March about code rewritten in July is worse than no
// assertion, because the doc now reads as current and confident.
//
// So a doc carries `verified_at: <sha>` alongside its claims: a statement that a person read this
// prose against the code as of that commit. If any anchor's SOURCE FILE has been touched since,
// the doc is stale — not necessarily wrong, but no longer vouched for by anybody.
//
// WHY THIS IS DELIBERATELY COARSE. It flags on any change to the source file, including a comment
// fix that could not possibly invalidate the prose. That produces false positives, and that is the
// correct trade: the alternative is inferring which edits are semantically material, which nothing
// here can do reliably, and a staleness check that under-reports is one that lets a genuinely wrong
// doc keep its stamp. Clearing a false positive costs a re-read and a new sha. Missing a true one
// costs a reader who trusts something false.
//
// It shares the anchor index with Context Threads by design (decision #135): one lookup answers
// both "this file changed, which docs are now stale?" and "this file changed, which recorded facts
// are now suspect?". Docs staleness and context staleness are the same problem.

import (
	"os/exec"
	"sort"
	"strings"
)

// Stale is one doc whose prose is no longer vouched for.
type Stale struct {
	Doc        string   // repo-relative doc path
	VerifiedAt string   // the sha the doc was last read against ("" = never stamped)
	Moved      []string // anchors whose source has been touched since
}

// LastTouched returns the most recent commit sha that modified path. Empty on any error — a file
// git cannot resolve is not evidence of staleness, and guessing in either direction would be worse
// than saying nothing.
func LastTouched(root, path string) string {
	out, err := exec.Command("git", "-C", root, "log", "-1", "--format=%H", "--", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// IsAncestor reports whether a is an ancestor of b — i.e. whether the doc's stamp predates the
// source change. This is the actual staleness question, and it is asked of git rather than of
// timestamps because commit dates can be rewritten and branches merge out of order.
func IsAncestor(root, a, b string) bool {
	if a == "" || b == "" || a == b {
		return false
	}
	return exec.Command("git", "-C", root, "merge-base", "--is-ancestor", a, b).Run() == nil
}

// SameCommit reports whether two revs name the same commit. It exists because a stamp is written by
// hand and is almost always ABBREVIATED, while LastTouched returns a full sha — so the obvious
// comparison (a == b) says "different" for a doc stamped at exactly the commit that last touched its
// source, and `merge-base --is-ancestor` then says "stale", because a commit is its own ancestor.
// The result was that stamping a doc at HEAD did not clear it. A stamp you cannot clear is a stamp
// nobody writes.
func SameCommit(root, a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	res := func(rev string) string {
		out, err := exec.Command("git", "-C", root, "rev-parse", rev+"^{commit}").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	ra, rb := res(a), res(b)
	return ra != "" && ra == rb
}

// CheckStaleness finds docs whose stamp predates a change to something they claim.
//
// A doc with claims but NO stamp is reported with VerifiedAt "" — unstamped is its own state, not
// a pass. "Nobody has ever vouched for this" and "somebody vouched for it and it has not moved
// since" are different facts, and collapsing them is how a doc drifts for a year unnoticed.
func CheckStaleness(root string, claims []Claim, stamps map[string]string, sourceOf map[string]string) []Stale {
	// One git call per distinct source file rather than per anchor: a doc page claims a dozen
	// anchors that usually share two or three sources.
	lastTouched := map[string]string{}
	touched := func(src string) string {
		if v, ok := lastTouched[src]; ok {
			return v
		}
		v := LastTouched(root, src)
		lastTouched[src] = v
		return v
	}

	var out []Stale
	for _, c := range claims {
		// GENERATED docs cannot go stale, and asking a human to vouch for a file a machine writes
		// is exactly the kind of ritual that trains people to stamp without reading. Their currency
		// is already guaranteed by the surface-drift check (`gc-surface -check`), which fails if a
		// generated artifact disagrees with the declarations it came from — a stronger guarantee
		// than a human stamp, not a weaker one.
		if c.Generated {
			continue
		}
		stamp := stamps[c.Doc]
		var moved []string
		for _, anchor := range c.Covers {
			src := sourceOf[anchor]
			if src == "" {
				continue // an anchor with no source file (a vocabulary term) cannot go stale this way
			}
			last := touched(src)
			if last == "" {
				continue
			}
			// Unstamped: everything it claims counts as unverified. Stamped: only anchors whose
			// source moved AFTER the stamp.
			if stamp == "" || (IsAncestor(root, stamp, last) && !SameCommit(root, stamp, last)) {
				moved = append(moved, anchor)
			}
		}
		if len(moved) > 0 {
			sort.Strings(moved)
			out = append(out, Stale{Doc: c.Doc, VerifiedAt: stamp, Moved: moved})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Doc < out[j].Doc })
	return out
}

// ParseStamps reads `verified_at: <sha>` from the same head-of-file window as the claims.
func ParseStamps(root string, dirs []string) (map[string]string, error) {
	claims, err := parseLines(root, dirs, "verified_at:")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for doc, v := range claims {
		if f := strings.Fields(v); len(f) > 0 {
			out[doc] = f[0]
		}
	}
	return out, nil
}
