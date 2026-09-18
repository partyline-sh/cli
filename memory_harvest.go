package main

// COMMITS AS MEMORY.
//
// A commit message is not code. It is a decision, written by the person who made it, at the
// moment they made it, in their own words — which is exactly what project memory wants and
// exactly what it was throwing away. Asking someone to re-type into `remember` what they just
// wrote in a commit is asking them to do the work twice, and the second time never happens.
//
// WHY A TRAILER RATHER THAN THE WHOLE LOG. Most commits in any repo are "wip", "fix typo",
// "bump deps". The session brief is capped at 12 facts and that cap is the only reason it gets
// read; fill it with sediment and people stop reading it, at which point the memory is worse
// than nothing. So a commit opts IN by carrying a trailer, the same way it opts into
// Signed-off-by. Precision over recall: a fact nobody chose to record is usually noise.
//
// WHY THIS RUNS LOCALLY. A teammate's commits live in a repo you do not have and never will.
// Harvesting therefore happens on the machine that HAS the clone, and publishes the extracted
// facts into the shared memory repo. The commit messages travel; the code does not. That is the
// same boundary the whole project model rests on, and this respects it rather than crossing it.
//
// IDS COME FROM THE COMMIT. Two people with the same repo cloned would otherwise each record the
// same commit as a separate fact. A fact's id is derived from the commit hash, so both machines
// produce byte-identical files and git merges them as the same file rather than two.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// trailerPrefix is what marks a line in a commit message as something to remember. One per kind,
// so the commit says what sort of thing it is rather than leaving it to be guessed.
const trailerPrefix = "Ptln-"

// harvested is one trailer found in one commit.
type harvested struct {
	Kind string
	Body string
}

// commitTrailers pulls the memory trailers out of a commit message.
//
// Git trailers live in the last paragraph, but commit messages in the wild are not that tidy —
// a Co-Authored-By block, a revert footer, or a body that runs on will all break the "last
// paragraph" rule. So this scans every line, which costs nothing and means a correctly written
// trailer is never silently ignored because of what happened to sit under it.
//
// A trailer continues onto following lines while they are indented, so a fact can be a sentence
// rather than whatever fits in one line.
func commitTrailers(msg string) []harvested {
	var out []harvested
	var cur *harvested
	for _, line := range strings.Split(msg, "\n") {
		// A continuation: indented, and we are inside a trailer.
		if cur != nil && strings.TrimSpace(line) != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			cur.Body += " " + strings.TrimSpace(line)
			continue
		}
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, trailerPrefix) {
			continue
		}
		rest := trimmed[len(trailerPrefix):]
		colon := strings.IndexByte(rest, ':')
		if colon <= 0 {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(rest[:colon]))
		if !validFactKind(kind) {
			continue // an unknown kind is a typo, not a new category
		}
		body := strings.TrimSpace(rest[colon+1:])
		cur = &harvested{Kind: kind, Body: body}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	// A trailer with a kind and no text is a mistake, not an empty fact.
	kept := out[:0]
	for _, h := range out {
		if strings.TrimSpace(h.Body) != "" {
			kept = append(kept, h)
		}
	}
	return kept
}

// commitFactID is stable across machines and across re-runs: the same commit and the same
// trailer always produce the same id, so harvesting twice writes the same file rather than a
// duplicate fact. It keeps the shape of newFactID so nothing downstream has to know the
// difference — sortable by time, then a short opaque suffix.
func commitFactID(sha string, index int, at time.Time) string {
	sum := sha256.Sum256([]byte(sha + "\x00" + fmt.Sprint(index)))
	return at.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(sum[:3])
}

// factsFromCommit turns one commit into the facts it declares.
func factsFromCommit(c commitInfo, repoName string) []fact {
	var out []fact
	for i, h := range commitTrailers(c.Message) {
		out = append(out, fact{
			ID:     commitFactID(c.SHA, i, c.At),
			Kind:   h.Kind,
			Repo:   repoName,
			By:     c.Author,
			At:     c.At,
			Commit: c.SHA,
			Body:   h.Body,
		})
	}
	return out
}
