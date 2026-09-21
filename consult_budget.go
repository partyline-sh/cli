package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// consult_budget.go — the owner-side spend bound on AUTO-answered consults.
//
// WHY THIS EXISTS. Auto-answer (consult_policy.go) removes the human from the loop, which means a
// teammate's agent can now make THIS machine run LLM turns without anyone here approving anything.
// The server-side rate limit is per-asker and per-minute (CONSULT_RATE_LIMIT = 20 per window), which
// bounds a burst but not a day: sustained, that is thousands of engine turns on my hardware and my
// token budget. A compromised teammate account, or an agent stuck in a retry loop, is not a
// hypothetical. So the machine that PAYS enforces its own daily bound, locally, in its own state.
//
// EXCEEDING IT IS NOT A FAILURE. Over the cap we fall back to the approval queue — the same queue
// that existed before auto-answer and still works. The question is not dropped and the asker is not
// errored; a human simply gets asked, which is exactly the right behaviour when the automatic
// allowance for the day is used up.
//
// WHAT IT IS KEYED ON, AND WHY NOT THE ASKER. Per-asker would be the sharper control, and it is not
// available: the consult push carries only {type, consult_id, project_label, question} — no asker
// identity (web/src/app/api/v1/daemon/stream/route.ts). The two ways to get one are both worse than
// what's here. Taking it from the request payload would let the requester choose their own bucket,
// which is the abuse vector this file exists to close. Reading it from the user-authed list endpoint
// would put a NETWORK CALL on the enforcement path of a spend control — one that then has to decide
// whether to fail open (uncapped) or closed (a control-plane hiccup silently disables auto-answer).
// So the ledger is keyed on what this machine can know for free and the asker cannot influence: the
// advertised project label, plus a machine-wide total so no single label's allowance can be spent
// over and over across labels. Both are bounds on MY cost, which is what the control is for.
//
// The ledger is a CACHE with a floor, not an audit log: losing it can only ever grant a fresh
// allowance, never charge someone twice, so a missing or corrupt file is an empty day.

// The defaults. A genuine human asks a peer a handful of times a day, and each auto-answer is one
// read-only engine turn (≤5 min, ≤16k of answer). 24 per project is already several times normal use;
// the machine-wide 48 is what stops a fan-out across many advertised labels from multiplying it.
// Both are overridable for a box that really is a shared review worker.
const (
	defaultConsultAutoDaily      = 24 // per advertised project, per day
	defaultConsultAutoDailyTotal = 48 // this machine, per day, across all projects

	// The HARD CEILINGS the control plane cannot exceed. The per-project number is now a PROJECT
	// setting edited in the web (projects.consult_auto_daily) so one edit governs every machine in the
	// project — but a setting that arrives over the network is not allowed to uncap this machine, so it
	// is clamped to these. High enough that the web setting is effectively authoritative for real use,
	// low enough that a compromised control plane buys bounded spend, not unbounded.
	hardConsultAutoDaily      = 200
	hardConsultAutoDailyTotal = 400

	// envConsultAutoDaily / envConsultAutoDailyTotal. These now BOUND rather than set: each is both
	// the fallback used when the project has no setting (preserving what an operator's override always
	// meant) AND the ceiling the project setting is clamped to — so a box can still tighten itself
	// below whatever the project asks for. 0 means "never auto-answer" — a legitimate setting, and the
	// reason this parses 0 rather than treating it as unset.
	envConsultAutoDaily      = "PARTYLINE_CONSULT_AUTO_DAILY"
	envConsultAutoDailyTotal = "PARTYLINE_CONSULT_AUTO_DAILY_TOTAL"
)

// consultAutoDailyCap resolves the effective per-project cap for a day:
//
//	effective = min(project setting, ceiling)   with ceiling = env override, else hardConsultAutoDaily
//
// FAILS CLOSED. A missing setting (nil — no project row, an old control plane, a NULL column) or a
// negative one falls back to the CURRENT default (or the env override), never to unlimited. The clamp
// only ever runs DOWNWARD: nothing on the wire can raise this machine past its own ceiling.
//
// project == 0 is honoured as zero: "never auto-answer in this project, always queue for a human".
func consultAutoDailyCap(project *int) int {
	return clampConsultCap(project, envInt(envConsultAutoDaily, defaultConsultAutoDaily), envInt(envConsultAutoDaily, hardConsultAutoDaily))
}

// consultAutoDailyTotalCap bounds the whole machine for the day, across every label. It has no project
// setting of its own (a machine-wide total is not a project's business), so it is the env override or
// the default — raised only far enough that a project which legitimately asks for more than 48 isn't
// silently swallowed by the total, and still clamped to hardConsultAutoDailyTotal.
func consultAutoDailyTotalCap(perProject int) int {
	total := envInt(envConsultAutoDailyTotal, defaultConsultAutoDailyTotal)
	if perProject > total {
		total = perProject
	}
	if ceiling := envInt(envConsultAutoDailyTotal, hardConsultAutoDailyTotal); total > ceiling {
		total = ceiling
	}
	return total
}

// clampConsultCap is the whole clamp rule, in one testable place: nil/negative → fallback, anything
// above the ceiling → the ceiling, otherwise the value as given (0 included).
func clampConsultCap(project *int, fallback, ceiling int) int {
	if project == nil || *project < 0 {
		return fallback
	}
	if *project > ceiling {
		return ceiling
	}
	return *project
}

// envInt reads a non-negative integer override. Anything unparseable is the default — a typo in an
// env var must not silently uncap (or zero) a spend control.
func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func consultBudgetPath() string {
	return filepath.Join(daemonDir(), "consult-budget.json")
}

// consultBudget is one day's auto-answer spend. LastProject/LastAt are not accounting — they are what
// lets `ptln state` (and so the tray) say "something was answered here, about <label>" without any
// surface having to read a question or an answer.
type consultBudget struct {
	Day         string         `json:"day"` // YYYY-MM-DD, local time — the rollover key
	Total       int            `json:"total"`
	Projects    map[string]int `json:"projects,omitempty"`
	LastProject string         `json:"last_project,omitempty"`
	LastAt      time.Time      `json:"last_at,omitempty"`
}

func budgetDay(now time.Time) string { return now.Format("2006-01-02") }

// loadConsultBudgetAt reads the ledger, rolling it over when the day has changed. Pure of side
// effects, so the rollover rule is testable without a clock or a filesystem.
func loadConsultBudgetAt(path string, now time.Time) consultBudget {
	b := consultBudget{Day: budgetDay(now), Projects: map[string]int{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return b
	}
	var on consultBudget
	if json.Unmarshal(raw, &on) != nil {
		return b
	}
	if on.Day != b.Day {
		return b // a new day is a new allowance; yesterday's counts are not kept
	}
	if on.Projects == nil {
		on.Projects = map[string]int{}
	}
	return on
}

func saveConsultBudgetAt(path string, b consultBudget) {
	raw, err := json.MarshalIndent(b, "", " ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600) // best-effort: a write failure must not stop the daemon
}

// claimConsultAutoAnswerAt charges one auto-answer against the day, returning whether it fits. On
// false it charges NOTHING and names the cap that was hit, so the caller can say why a question went
// to the human queue instead of being answered.
func claimConsultAutoAnswerAt(path, label string, project *int, now time.Time) (bool, string) {
	b := loadConsultBudgetAt(path, now)
	perProject := consultAutoDailyCap(project)
	total := consultAutoDailyTotalCap(perProject)
	if b.Total >= total {
		return false, "this machine's daily auto-answer cap (" + strconv.Itoa(total) + ") is used up"
	}
	if b.Projects[label] >= perProject {
		return false, "today's auto-answer cap for " + label + " (" + strconv.Itoa(perProject) + ") is used up"
	}
	b.Total++
	b.Projects[label]++
	b.LastProject, b.LastAt = label, now
	saveConsultBudgetAt(path, b)
	return true, ""
}

var consultBudgetMu sync.Mutex

// claimConsultAutoAnswer is the locked, default-path claim the daemon calls. `project` is the
// project-wide setting from the control plane (nil = none; see consultAutoDailyCap for the clamp).
//
// THE CAP IS PROJECT-WIDE; THE SPEND IS PER MACHINE. This ledger is local and always will be: three
// machines advertising the same label, each with a cap of 24, can collectively answer 72 questions in
// a day. A single shared allowance would need server-side counting on the enforcement path, which is
// exactly what this file's header refuses to do. Say the number out loud in the UI instead.
func claimConsultAutoAnswer(label string, project *int) (bool, string) {
	consultBudgetMu.Lock()
	defer consultBudgetMu.Unlock()
	return claimConsultAutoAnswerAt(consultBudgetPath(), label, project, time.Now())
}

// readConsultBudget is the read-only view `ptln state` uses. LOCAL FILE ONLY — see state.go.
func readConsultBudget() consultBudget {
	consultBudgetMu.Lock()
	defer consultBudgetMu.Unlock()
	return loadConsultBudgetAt(consultBudgetPath(), time.Now())
}
