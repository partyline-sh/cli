// Package gate is the typed outcome of verifying one task's branch.
//
// WHY THIS EXISTS. The verify gate used to return this:
//
//	type verifyResult struct {
//		ran, ok       bool
//		reasons, warn string
//	}
//
// Two booleans and two prose blobs — which is why five separate improvements were all blocked, and
// all blocked in the same way. There was nowhere to record that a reviewer proved it did not touch
// the code it judged; nowhere to say a failure was a rate limit rather than a wrong diff; no room
// for a SECOND reviewer lane; no field a worker could be prevented from writing; and no per-check
// granularity, so severity and path scoping had nothing to attach to.
//
// So this is one contract, and the features are thin on top of it.
//
// DESIGN NOTES.
//
//   - Versioned. Reports are persisted as jsonb on run_tasks and read back by a control plane that
//     may be newer or older than the daemon that wrote them. A shape with no version is a shape you
//     cannot change.
//   - Codes are a closed vocabulary (internal/surface), not free text. That is what makes a failure
//     classifiable, documentable, and translatable into a UI action without a human reading prose.
//   - A Report says what happened. It does not decide what to DO — that is the caller's job, and
//     keeping the split means the same report drives crank's quarantine decision, the board's card,
//     and the ledger entry without any of them re-deriving it differently.
package gate

import (
	"time"

	"partyline.sh/partyline/internal/surface"
)

// Version is the current report shape. Bump it when a field changes meaning; readers should treat
// an unknown (higher) version as "I can read the fields I know", never as an error, because a
// daemon in the fleet WILL be newer than the control plane during a rollout.
const Version = 1

// Kind identifies which layer of the gate produced a result.
const (
	KindCheck    = "check"    // T2a — the repo's own executable acceptance checks
	KindReviewer = "reviewer" // T2b — an independent adversarial reviewer, tool-less
	KindVisual   = "visual"   // T2d — a vision reviewer that renders the change and looks at it
	KindReadOnly = "readonly" // the proof that a judging lane did not modify what it judged
)

// Status is what happened to one lane.
const (
	StatusPass = "pass"
	StatusFail = "fail"
	StatusWarn = "warn" // advisory: recorded, surfaced, never blocking
	StatusSkip = "skip" // not enabled for this repo — honestly absent, NOT a pass
)

// Evidence is compact source material a human or a later reviewer can check the claim against.
// Bounded on purpose: the ledger is not a log, and a chatty check must not be able to bloat it.
type Evidence struct {
	Kind  string `json:"kind,omitempty"`  // "command", "file", "url", "diff"
	Path  string `json:"path,omitempty"`  //
	Value string `json:"value,omitempty"` // bounded — see Truncate
	Note  string `json:"note,omitempty"`
}

// Finding is one problem a reviewer raised, normalised so findings from DIFFERENT lanes can be
// merged and compared. Lanes records every lane that independently raised it — the agreement
// signal, and the single most useful way to rank a review queue: two reviewers converging on the
// same line is far stronger evidence than one.
type Finding struct {
	Title    string     `json:"title,omitempty"`
	Body     string     `json:"body,omitempty"`
	File     string     `json:"file,omitempty"`
	Line     int        `json:"line,omitempty"`
	Severity string     `json:"severity,omitempty"`
	Lanes    []string   `json:"lanes,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

// ReadOnlyProof records whether a judging lane demonstrably left the worktree alone.
//
// Three signals, not one. Checking `git status` alone — which is the obvious implementation, and
// the one a comparable system ships — misses a reviewer that COMMITS its edits: the tree comes back
// clean and the mutation is invisible. HeadBefore/HeadAfter closes that, and the stash count closes
// the variant where work is parked rather than committed.
type ReadOnlyProof struct {
	Observed     bool     `json:"observed"` // we actually took a baseline; false = no claim made
	Passed       bool     `json:"passed"`
	StatusBefore string   `json:"status_before,omitempty"` // hash of `git status --porcelain=v1 -z`
	StatusAfter  string   `json:"status_after,omitempty"`
	HeadBefore   string   `json:"head_before,omitempty"`
	HeadAfter    string   `json:"head_after,omitempty"`
	StashBefore  int      `json:"stash_before,omitempty"`
	StashAfter   int      `json:"stash_after,omitempty"`
	Changed      []string `json:"changed,omitempty"` // the files that moved, as evidence
}

// CheckResult is one lane's outcome.
type CheckResult struct {
	Name     string     `json:"name"`     // "build", "test", "reviewer:primary"
	Kind     string     `json:"kind"`     // KindCheck | KindReviewer | KindVisual | KindReadOnly
	Status   string     `json:"status"`   // StatusPass | StatusFail | StatusWarn | StatusSkip
	Code     string     `json:"code"`     // a surface.GateCode key
	Class    string     `json:"class"`    // derived from Code; stored so a reader needs no lookup table
	Blocking bool       `json:"blocking"` // false = advisory; a failure is recorded, never quarantines
	Detail   string     `json:"detail,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Lane     string     `json:"lane,omitempty"` // quorum lane id, when there is more than one
	Engine   string     `json:"engine,omitempty"`
	Model    string     `json:"model,omitempty"`
	Millis   int64      `json:"millis,omitempty"`
	Tokens   int        `json:"tokens,omitempty"`
}

// Report is the whole gate outcome for one task.
type Report struct {
	Version  int           `json:"version"`
	Verdict  string        `json:"verdict"` // a surface.GateVerdict key
	Code     string        `json:"code,omitempty"`
	Class    string        `json:"class,omitempty"`
	Checks   []CheckResult `json:"checks,omitempty"`
	Findings []Finding     `json:"findings,omitempty"`
	ReadOnly ReadOnlyProof `json:"read_only"`
	Started  time.Time     `json:"started,omitempty"`
	Millis   int64         `json:"millis,omitempty"`
	// Review records what the reviewer lanes did, so the blocking rule can be chosen from evidence
	// instead of from argument. See ReviewStats.
	Review *ReviewStats `json:"review,omitempty"`
}

// ReviewStats is the measurement that settles G.5's open question.
//
// The blocking rule (fail on ANY rejection, or only on agreement) was picked by reasoning, and
// reasoning is not enough: under fail-any a second lane strictly increases the quarantine rate, so
// a noisier reviewer costs a human exactly the minutes the epic exists to save. Whether that
// happens is an empirical question about YOUR models on YOUR diffs.
//
// These fields make it answerable without new plumbing. Everything needed is already in the run
// store once this rides along:
//
//	quarantine rate      = tasks with gate_verdict 'fail' ÷ all gated tasks
//	FALSE-quarantine rate = quarantined tasks a human ACCEPTED anyway ÷ quarantined tasks
//	                        (the accept already exists; no new write is needed to count it)
//	would-have-blocked   = tasks where LoneDissent is true — exactly the set the default let
//	                        through and fail-any would have stopped
//
// That last one is the crux: run the default for a fortnight, then ask how many lone dissents were
// real defects a human acted on versus noise they scrolled past.
type ReviewStats struct {
	Policy      string `json:"policy"`                 // the FailPolicy in force
	Lanes       int    `json:"lanes"`                  // lanes configured
	Judged      int    `json:"judged"`                 // lanes that reached a verdict
	Rejects     int    `json:"rejects"`                // lanes that rejected
	Agreed      int    `json:"agreed,omitempty"`       // findings more than one lane raised
	Findings    int    `json:"findings,omitempty"`     // findings after merging
	LoneDissent bool   `json:"lone_dissent,omitempty"` // one lane rejected, others judged and did not
	Tokens      int    `json:"tokens,omitempty"`       // total reviewer spend for this task
}

// SummarizeReview builds the stats block from a set of lane results.
func SummarizeReview(lanes []LaneResult, policy FailPolicy, merged []Finding) *ReviewStats {
	if len(lanes) == 0 {
		return nil
	}
	st := &ReviewStats{Policy: string(policy), Lanes: len(lanes), Findings: len(merged)}
	for _, l := range lanes {
		st.Tokens += l.Tokens
		if l.Verdict == surface.VerdictBlocked {
			continue
		}
		st.Judged++
		if l.Verdict == surface.VerdictFail {
			st.Rejects++
		}
	}
	for _, f := range merged {
		if len(f.Lanes) > 1 {
			st.Agreed++
		}
	}
	st.LoneDissent = st.Rejects == 1 && st.Judged > 1
	return st
}

// New returns an empty report at the current version.
func New() *Report { return &Report{Version: Version, Verdict: surface.VerdictSkipped} }

// Add appends a lane result, filling in Class from the declared code so callers cannot record a
// disposition that disagrees with the vocabulary.
func (r *Report) Add(c CheckResult) {
	c.Class = string(surface.GateCode.ClassOf(c.Code))
	r.Checks = append(r.Checks, c)
}

// Finalize computes the report's verdict from its lanes and returns it.
//
// The rules, in precedence order:
//
//  1. A blocking lane failed with a HARD code → fail. This is the quarantine.
//  2. A lane failed with a TRANSIENT code → blocked. The diff was never judged, so this is neither
//     a pass nor a rejection of the work; it is "ask again later", and it resolves without a human.
//  3. Findings, nothing blocking → pass_with_findings. Merges, and the findings ride on the PR.
//  4. Something ran and passed → pass.
//  5. Nothing was enabled → skipped. Deliberately NOT pass: a repo that configured no checks has
//     proved nothing, and calling that a pass is how "green" stops meaning anything.
//
// WHY HARD BEATS TRANSIENT, rather than the other way round. Both can be true at once — the build
// genuinely failed AND the reviewer was rate-limited. Retrying cannot fix a failed build, so
// reporting `blocked` there would park a definitely-broken branch waiting for a quota that will
// change nothing. Definite bad news outranks "we do not know".
//
// Getting this precedence backwards was the first version of this function, and the rate-limit
// case in report_test.go is what caught it.
func (r *Report) Finalize() string {
	var ran, hardFail, transient bool
	var failCode, failClass string
	for _, c := range r.Checks {
		if c.Status == StatusSkip {
			continue
		}
		ran = true
		if c.Status != StatusFail {
			continue
		}
		switch {
		case c.Class == string(surface.ClassTransient):
			transient = true
			if failCode == "" {
				failCode, failClass = c.Code, c.Class
			}
		case c.Blocking && !hardFail:
			hardFail, failCode, failClass = true, c.Code, c.Class
		}
	}
	switch {
	case hardFail:
		r.Verdict, r.Code, r.Class = surface.VerdictFail, failCode, failClass
	case transient:
		r.Verdict, r.Code, r.Class = surface.VerdictBlocked, failCode, string(surface.ClassTransient)
	case ran && len(r.Findings) > 0:
		r.Verdict = surface.VerdictPassWithFindings
	case ran:
		r.Verdict = surface.VerdictPass
	default:
		r.Verdict = surface.VerdictSkipped
	}
	return r.Verdict
}

// Ran reports whether any lane was enabled. The distinction the old `ran` bool carried, preserved
// because the merge train depends on it: a repo with no gate has not earned an automatic landing.
func (r *Report) Ran() bool {
	for _, c := range r.Checks {
		if c.Status != StatusSkip {
			return true
		}
	}
	return false
}

// OK reports whether the branch may proceed to merge.
func (r *Report) OK() bool {
	return r.Verdict == surface.VerdictPass || r.Verdict == surface.VerdictPassWithFindings
}

// Reasons renders the blocking failures for a human, in the order they were recorded.
func (r *Report) Reasons() string {
	var out []string
	for _, c := range r.Checks {
		if c.Status == StatusFail && c.Blocking {
			out = append(out, c.Name+": "+c.Detail)
		}
	}
	return join(out, "\n")
}

// Warnings renders the advisory results — a check that is red on the base branch too, a visual
// lane with no renderer. Surfaced on the task, never a quarantine.
func (r *Report) Warnings() string {
	var out []string
	for _, c := range r.Checks {
		if c.Status == StatusWarn {
			out = append(out, c.Detail)
		}
	}
	return join(out, "; ")
}

func join(parts []string, sep string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += sep
		}
		s += p
	}
	return s
}

// Truncate bounds a string for Evidence and Detail. The ledger is queried and rendered; an
// unbounded check log in a jsonb column is a performance problem and an unreadable UI.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
