package gate

import (
	"sort"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/surface"
)

// G.5 — more than one reviewer, and agreement as a ranking signal.
//
// WHY THIS IS THE SLICE THAT MATTERS. Every other part of the gate makes review more TRUSTWORTHY.
// This one makes it CHEAPER, which is the only number the business question turns on: minutes of
// human attention per merged pull request. A single reviewer gives you a list of findings in
// arbitrary order and no way to tell a real defect from a stylistic opinion. Two independent
// reviewers give you something a single one cannot, at any quality: AGREEMENT.
//
// A finding both models raised, independently, from the same diff, is far likelier to be real than
// one raised by either alone. That is not a claim about model quality — it is what independence
// buys. So the merge exists to compute agreement, and the ordering exists to put it first. A human
// reading a review queue top-down should hit the things two reviewers converged on before they hit
// one reviewer's stylistic preference.
//
// The cost is honest and worth stating: two lanes is 2× reviewer tokens for the same diff. Lanes
// run concurrently, so wall-clock is roughly unchanged, and per-lane spend is recorded so the
// trade is measured rather than assumed.

// LaneResult is one reviewer's verdict on one diff.
type LaneResult struct {
	Lane     string
	Engine   string
	Model    string
	Verdict  string // pass | fail | blocked (blocked = could not judge, not "rejected")
	Findings []Finding
	Code     string
	Detail   string
	Tokens   int
	Millis   int64
}

// MergeFindings folds every lane's findings into one list, joined where two lanes are talking about
// the same thing, and ordered so agreement comes first.
//
// The join key is deliberately fuzzy on line number. Two models reading the same diff will often
// describe the same defect a few lines apart — one points at the call, the other at the assignment
// above it — and treating those as separate findings would destroy the exact signal this exists to
// produce. ±3 lines is narrow enough not to merge genuinely different problems in dense code.
//
// Ordering: most lanes first (agreement), then severity, then file/line so the result is stable and
// a diff of two runs is readable.
func MergeFindings(lanes []LaneResult) []Finding {
	var keys []string
	byKey := map[string]*mergeSlot{}

	for _, l := range lanes {
		for _, f := range l.Findings {
			k, existing := findingKey(f, byKey, keys)
			if existing == "" {
				byKey[k] = &mergeSlot{f: f, line: f.Line, lanes: map[string]bool{l.Lane: true}}
				keys = append(keys, k)
				continue
			}
			s := byKey[existing]
			s.lanes[l.Lane] = true
			// Keep the fuller description. Two reviewers rarely write the same length, and the
			// longer one usually carries the reasoning a human needs.
			if len(f.Body) > len(s.f.Body) {
				s.f.Body = f.Body
			}
			if severityRank(f.Severity) > severityRank(s.f.Severity) {
				s.f.Severity = f.Severity
			}
			s.f.Evidence = append(s.f.Evidence, f.Evidence...)
		}
	}

	out := make([]Finding, 0, len(keys))
	for _, k := range keys {
		s := byKey[k]
		f := s.f
		f.Lanes = sortedKeys(s.lanes)
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Lanes) != len(out[j].Lanes) {
			return len(out[i].Lanes) > len(out[j].Lanes) // agreement first
		}
		if severityRank(out[i].Severity) != severityRank(out[j].Severity) {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// mergeSlot is one accumulating finding plus the lanes that raised it.
type mergeSlot struct {
	f     Finding
	line  int
	lanes map[string]bool
}

// findingKey returns the key for a finding and, when an existing near-match is present, that
// existing key — which is how two lanes describing one defect a few lines apart end up joined.
func findingKey(f Finding, byKey map[string]*mergeSlot, keys []string) (fresh, existing string) {
	base := f.File + "|" + normalizeTitle(f.Title)
	for _, k := range keys {
		if !strings.HasPrefix(k, base+"|") {
			continue
		}
		if near(byKey[k].line, f.Line) {
			return base, k
		}
	}
	return base + "|" + strconv.Itoa(f.Line), ""
}

func near(a, b int) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 3
}

// normalizeTitle strips the incidental differences between two models describing one defect:
// case, punctuation, and filler. Two titles that reduce to the same words are the same finding.
func normalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	var keep []string
	for _, w := range words {
		if !filler[w] {
			keep = append(keep, w)
		}
	}
	sort.Strings(keep) // word ORDER differs between models; the set is what identifies the defect
	return strings.Join(keep, " ")
}

var filler = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "of": true, "to": true,
	"in": true, "on": true, "this": true, "that": true, "it": true, "and": true, "for": true,
}

func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "critical", "high":
		return 3
	case "medium", "moderate":
		return 2
	case "low", "minor", "nit":
		return 1
	}
	return 0
}

// FailPolicy decides when a rejection BLOCKS rather than merely being reported.
//
// This is a setting rather than a constant because we do not know which is right, and the honest
// version of "we do not know" is a default plus a way to measure. The first draft hard-coded
// FailAny and justified it as "one reviewer finding a real defect is enough". That reasoning is
// sound and the conclusion may still be wrong: adding a second lane under FailAny strictly
// INCREASES the quarantine rate, so a noisier second reviewer costs a human exactly the minutes
// this epic exists to save. The cheapening comes from RANKING, not from the verdict rule; conflating
// the two was the error.
type FailPolicy string

const (
	// FailOnAgreement (the default): a rejection blocks when more than one lane rejects, OR when
	// only one lane judged at all. A LONE objection among several judges does not block — it
	// becomes a visible finding on the pull request, where the human reviewing it will see it.
	// Recall barely drops, because nothing is discarded; only the blocking threshold moves.
	FailOnAgreement FailPolicy = "agreement"
	// FailAny: any rejection blocks. Maximum recall, highest false-quarantine rate. Correct for a
	// project that would rather stop than let anything through.
	FailAny FailPolicy = "any"
)

// Quorum decides the gate's reviewer verdict from every lane.
//
// The rules, and why each is what it is:
//
//   - Any lane that REJECTED → fail. One reviewer finding a real defect is enough; requiring two to
//     agree before blocking would mean a defect only one model spotted ships.
//   - Otherwise, all lanes BLOCKED → blocked. Nobody judged the diff, so there is no verdict to
//     report and fail-closed applies.
//   - Otherwise, findings but no rejection → pass_with_findings. The reviewers raised things and
//     chose not to block; the branch merges and the findings ride on the pull request.
//   - Otherwise → pass.
//
// Note the asymmetry: one lane can FAIL the gate, but one lane cannot RESCUE it. That is
// deliberate. Agreement is a ranking signal for humans, not a voting threshold for blocking —
// treating it as a vote would mean adding a second, weaker reviewer makes the gate weaker.
func Quorum(lanes []LaneResult) (verdict, code string) { return QuorumWith(lanes, FailOnAgreement) }

// QuorumWith is Quorum under an explicit policy.
func QuorumWith(lanes []LaneResult, policy FailPolicy) (verdict, code string) {
	if len(lanes) == 0 {
		return surface.VerdictSkipped, surface.CodeSkipped
	}
	judged, findings, rejects := 0, 0, 0
	rejectCode := ""
	for _, l := range lanes {
		switch l.Verdict {
		case surface.VerdictFail:
			judged++
			rejects++
			if rejectCode == "" {
				rejectCode = firstNonEmptyCode(l.Code, surface.CodeReviewerRejected)
			}
			findings += len(l.Findings)
		case surface.VerdictBlocked:
			continue
		default:
			judged++
			findings += len(l.Findings)
		}
	}
	// A rejection blocks under FailAny always; under FailOnAgreement only when it is not a lone
	// dissent among several judges. With a single judge there is nobody to agree with, so its
	// rejection stands — otherwise turning on a second lane and having it fail would WEAKEN the
	// one-reviewer gate, which is the trap this whole design is trying to avoid.
	if rejects > 0 && (policy == FailAny || rejects > 1 || judged <= 1) {
		return surface.VerdictFail, rejectCode
	}
	if judged == 0 {
		// Every lane failed to reach a judgment. Report the first lane's reason rather than a
		// generic one — "the reviewer timed out" and "unknown engine" need different fixes.
		return surface.VerdictBlocked, firstNonEmptyCode(lanes[0].Code, surface.CodeReviewerTimeout)
	}
	// A lone dissent that did not block is not discarded — it rides onto the pull request as a
	// finding, which is where the human deciding whether to merge will actually read it.
	if findings > 0 || rejects > 0 {
		return surface.VerdictPassWithFindings, surface.CodeOK
	}
	return surface.VerdictPass, surface.CodeOK
}

// Agreement reports how many lanes independently raised the given finding, and how many lanes
// actually REACHED A JUDGMENT — the denominator.
//
// The denominator is not cosmetic. If a project configures two lanes and one is permanently
// rate-limited, reporting bare agreement badges "1/1 reviewers" on every finding: maximum
// confidence, from a gate that silently lost half its reviewers. That is the same failure this
// epic bans everywhere else — "we did not check" must never read as "it passed" — and the first
// version of this function committed it.
//
// So callers get both numbers and the UI can say "1 of 2 · one reviewer did not run", which is the
// truth. Judged counts lanes that produced a verdict, NOT lanes configured: a lane that could not
// run contributed no opinion and must not inflate the denominator's meaning either.
func Agreement(f Finding, lanes []LaneResult) (agreed, judged int) {
	for _, l := range lanes {
		if l.Verdict != surface.VerdictBlocked {
			judged++
		}
	}
	return len(f.Lanes), judged
}

// Degraded reports lanes that were configured but never reached a judgment. A gate running on
// fewer reviewers than the project asked for is a fact the operator needs; discovering it months
// later from a token bill is not good enough.
func Degraded(lanes []LaneResult) []LaneResult {
	var out []LaneResult
	for _, l := range lanes {
		if l.Verdict == surface.VerdictBlocked {
			out = append(out, l)
		}
	}
	return out
}

func firstNonEmptyCode(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return surface.CodeReviewerRejected
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
