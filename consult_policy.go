package main

import (
	"os"
	"path/filepath"
	"strings"
)

// consult_policy.go — the privilege to auto-answer a peer's QUESTION, separated from the privilege to
// auto-launch WRITE work.
//
// THE CONFLATION THIS UNDOES. `isAutoProject` is `launchPolicy() == "auto"`, and that one flag gated
// both of these:
//
//	daemon.go — auto-accepting a launch request and auto-queuing a RUN: a full autonomous agent that
//	            writes code in a worktree, commits, and opens a PR on this machine.
//	daemon.go — auto-answering a CONSULT: one read-only engine turn that produces a paragraph of
//	            feedback and can neither write a file nor run a command (P0.0-enforced).
//
// Those are not the same privilege and should never have shared a switch. Requiring full Auto — "let
// teammates' agents write code here unattended" — just to let them ask a question was backwards, so
// the honest fix is a second, strictly weaker setting rather than advice to flip the strong one.
//
// THE DIRECTION OF IMPLICATION IS ONE-WAY. Auto launch implies auto answer, because a machine that
// already accepts unattended write runs cannot coherently insist on a human for a read-only question.
// Auto answer does NOT imply auto launch. That asymmetry is the whole point.
//
// WHY THE DEFAULT IS ON. This was a decision, not an oversight, and these are its grounds:
//   - the answer turn is READ-ONLY and that is enforced by the engine posture, not by convention
//     (consult_answer.go runConsultAnswer) — it cannot mutate the checkout or run a command
//   - the question is bounded (MAX_QUESTION = 8000) and the answer is bounded (maxConsultAnswer)
//   - only teammates can ask: the consult route is org-scoped, so this is not open to the internet
//   - the asker is rate-limited server-side, and capped again locally per day (consult_budget.go),
//     with the overflow going to the human queue rather than being dropped
//   - every consult leaves a durable row, so an auto-answer is reviewable after the fact
//
// A question you can't cost me anything by asking, that I can read the history of, is a poor thing to
// interrupt a human for. What is NOT defensible is inferring that from the launch flag, or making it
// unswitchable — hence a real per-project OFF, which restores exactly today's approval queue.

// consultPolicy normalizes a project's consult auto-answer policy: "auto" (answer a teammate's
// read-only question without asking me) or "ask" (queue it for my approval, the pre-auto-answer
// behaviour). Empty/unknown → "auto", the documented default above. "ask" is the only value that
// turns it off, so a garbled registry field can never quietly disable a feature the owner enabled —
// and, symmetrically, the owner's explicit "ask" is honoured even on a full-Auto project, because an
// explicit local decision outranks an implication.
func (p daemonProject) consultPolicy() string {
	if p.Consults == "ask" {
		return "ask"
	}
	return "auto"
}

// ---- the machine-wide OFF switch -----------------------------------------------------------------

// WHY THIS IS A FILE AND NOT AN ENVIRONMENT VARIABLE. The machine-wide off switch used to be
// documented as PARTYLINE_CONSULT_AUTO_DAILY=0, and for the common install that switch did nothing:
// `ptln daemon install` bakes only PATH (and PARTYLINE_API) into the LaunchAgent/systemd unit, so an
// export in your shell never reaches the always-on daemon that is actually answering. A safety switch
// that silently does nothing is worse than no switch at all, because the docs point people at it. So
// the real switch is persisted local state the ANSWERING process re-reads on every decision:
//   - it works for `ptln daemon run` and for the installed service identically
//   - flipping it takes effect on the next inbound question — no reinstall, no restart
//   - it lives with the daemon's other operator switches (autoupdate.on, provision.on) under
//     ~/.partyline/daemon/, 0600, never mirrored to the control plane
//
// The env var still works where it is read (consult_budget.go, as a cap of 0), and install now bakes
// it into the unit too — but that is a convenience, not the safety property.

// consultGlobalPolicyPath is the machine-wide auto-answer switch. A value file rather than a bare
// marker (autoupdate.on) so `cat` tells you which way it is set, and so "explicitly auto" is
// distinguishable from "never touched".
func consultGlobalPolicyPath() string {
	return filepath.Join(stateDir(), "daemon", "consults-global.mode")
}

// globalConsultPolicyAt reads the machine-wide switch: "auto" (this machine may auto-answer, subject to
// each project's own setting) or "ask" (nothing is auto-answered here, whatever any project says).
//
// FAILS CLOSED, DELIBERATELY. Only two things yield "auto": no file at all (the untouched default,
// which preserves the shipped behaviour) and a file whose contents are exactly "auto". Everything
// else — a truncated write, a garbled value, a file we cannot read because of its permissions — is
// "ask". This is the opposite of consultPolicy()'s garbled-value rule and of the budget ledger's
// tolerance for junk, and the asymmetry is the point: for a per-project field or a spend cache, a bad
// value must not silently disable a feature the owner enabled; for the OFF switch itself, a bad value
// must never silently RE-ENABLE what the owner turned off. A half-written file resolves toward safety.
func globalConsultPolicyAt(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "auto" // never set — today's default
		}
		return "ask" // present but unreadable: assume it says off
	}
	if strings.TrimSpace(string(raw)) == "auto" {
		return "auto"
	}
	return "ask"
}

// globalConsultPolicy reads the switch from this machine's state dir. Called per decision — see the
// re-read note on autoAnswerConsults.
func globalConsultPolicy() string { return globalConsultPolicyAt(consultGlobalPolicyPath()) }

func setGlobalConsultPolicyAt(path, mode string) error {
	if mode != "auto" {
		mode = "ask" // only "auto" is permissive; anything else is written as the off state
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(mode+"\n"), 0o600)
}

func setGlobalConsultPolicy(mode string) error {
	return setGlobalConsultPolicyAt(consultGlobalPolicyPath(), mode)
}

// autoAnswerConsults reports whether a consult for this label may be answered without the owner.
//
// PRECEDENCE, AND IT IS ONE-WAY: the machine-wide "ask" beats a project's "auto". A global switch that
// a per-project setting could override is not a safety switch — the operator who turns auto-answer off
// for the box must not have to audit every registered project, now or after the next `add-project`.
// The reverse does NOT hold: global "auto" is only permission to consider a project's own setting, so
// a project's explicit "ask" still wins. Both directions run toward the queue; neither can grant.
//
// RE-READ, NOT CACHED. Both the global switch and the registry are read here, on every decision, so
// flipping either takes effect on the next question rather than at the next daemon restart. The
// always-on service is the common install and nobody should have to restart it to stop this.
//
// LOCAL ONLY, AND DELIBERATELY TAKES A LABEL RATHER THAN AN EVENT. The decision is read from THIS
// machine's registry, keyed on a label the registry itself has to contain (an unresolvable label is
// declined upstream, before we get here). Nothing the asker sends is consulted: no field on the
// consult event, no server-sent flag, nothing derived from the question text. That is the security
// property — a requester must not be able to influence whether their own request is auto-approved —
// and the signature is the thing that enforces it, so keep it that way. If a future caller wants to
// pass an api.ConsultEvent in here, the answer is no.
func autoAnswerConsults(label string) bool {
	if globalConsultPolicy() != "auto" {
		return false // machine-wide OFF — no project can opt back in
	}
	p := projectByLabel(loadDaemonRegistry(), label)
	if p == nil {
		return false // not ours to answer at all
	}
	return p.consultPolicy() == "auto"
}

// consultDisposition is what this machine will do with an inbound question.
type consultDisposition int

const (
	consultVerdictDecline    consultDisposition = iota // we don't advertise the label — free the asker now
	consultVerdictAutoAnswer                           // one read-only turn, no human, budget already charged
	consultVerdictQueue                                // ask the owner (policy off, or the day's allowance is spent)
)

// decideConsult is the whole inbound policy decision, in one place. Its arguments are the advertised
// project label and a SPEND NUMBER — nothing that could flip the verdict itself. That signature is the
// security property made structural: the answer to "can a requester get their own request
// auto-approved?" is no by construction rather than by careful reading. The gating inputs are this
// machine's registry and this machine's daily ledger, both local files it owns.
//
// A consultVerdictAutoAnswer verdict has ALREADY CHARGED the budget, so the caller must either answer or
// accept the charge; do not call this speculatively.
//
// ORDER MATTERS: policy first, budget second. The caps (PARTYLINE_CONSULT_AUTO_DAILY, and now the
// project's own consult_auto_daily) are a SPEND bound, reached only once policy has already said yes,
// so no cap value — however large — can override a persisted OFF. Setting the cap to 0 remains a
// second way to stop auto-answer wherever the daemon can see it; the persisted switch works regardless.
//
// THE SECOND ARGUMENT IS A NUMBER, AND ONLY A NUMBER. projectCap is the project-wide allowance the
// control plane resolved from project settings — how MUCH this machine will spend, never WHETHER it may
// spend at all. It is read after the policy gate, is clamped downward to this machine's own ceiling
// (consult_budget.go), and cannot be sourced from the asker's payload. That is why it does not
// reintroduce the hazard the one-argument signature was protecting: an api.ConsultEvent still does not
// belong in here, and neither does anything derived from the question.
func decideConsult(label string, projectCap *int) (consultDisposition, string) {
	if projectByLabel(loadDaemonRegistry(), label) == nil {
		return consultVerdictDecline, "this machine doesn't have that project"
	}
	if !autoAnswerConsults(label) {
		return consultVerdictQueue, ""
	}
	if ok, why := claimConsultAutoAnswer(label, projectCap); !ok {
		return consultVerdictQueue, why // the allowance is spent — a human can still say yes
	}
	return consultVerdictAutoAnswer, ""
}
