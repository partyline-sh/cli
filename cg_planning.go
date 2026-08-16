package main

// cg_planning.go — PLANNING MODE for a CLI LLM session. See docs/epics/cli-planning-mode.md.
//
// THE PROBLEM. A user in a terminal session works out what needs building, and to get it into
// partyline has to stop, open the web app, and re-explain it to the planning agent. Nobody does
// that, so the work either never lands or lands as a one-line task crank cannot build.
//
// WHY THE MCP PROMPT DID NOT SOLVE IT. `/planning_agent` injects a wall of text ONCE. It is advice:
// it competes with everything that arrives after it, it restricts nothing, and there is no gate on
// the way out — the model can wander off and file whatever it happens to be holding. Twenty turns
// later the persona is a distant memory and the write tool is still callable.
//
// A mode has three properties a prompt cannot have: it is STICKY, it CONSTRAINS, and its EXIT
// REFUSES until the information is complete. An MCP server cannot change the host's system prompt
// or revoke the host's tools, so we do not imitate Claude Code's plan mode. We build the version a
// server CAN enforce, which is stronger where it matters: a prompt asks the model to behave; a
// server declines to write.
//
//	FRONT  step-wise delivery — one slot's instructions at the moment that slot comes up, so the
//	       guidance is always fresh and small and cannot decay.
//	BACK   the write gate — planning_finalize refuses. Front-loading alone can be walked away from:
//	       nothing stops a model from not asking for the next step, or a user jumping in halfway.
//
// THE SLOTS ARE THE GATE. The mode does not invent its own idea of "ready". computeSpecificity
// (specificity.ts) already defines it and /work-items/[id]/start already enforces it, so the agenda
// IS the server's blocking checks, recomputed after every answer. Nothing can be planned that
// cannot be started, and the CLI and web can never disagree about what "specified" means.
//
// WHERE THE DRAFT LIVES. On this machine, under the daemon dir, keyed by thread. It is per-session
// planning state, not shared team data — it becomes shared at finalize, when the tree is filed. The
// file (rather than memory alone) is what makes the mode survive the thing that kills long planning
// conversations: context compaction. The model can forget it was ever planning; the draft cannot.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// planDraft is the work-in-progress plan. Shaped like the tree it will become, so finalize is a
// straight conversion rather than an interpretation step.
type planDraft struct {
	Thread   string                  `json:"thread"`
	Idea     string                  `json:"idea"` // the opening statement or pasted PRD, kept verbatim
	Kind     string                  `json:"kind"` // epic | feature | task — an OUTPUT of decomposition
	Title    string                  `json:"title"`
	Document string                  `json:"document"` // the HOW: targets, behaviour, edge cases, guardrails
	Criteria []api.WorkItemCriterion `json:"criteria"`
	Children []api.WorkTreeNode      `json:"children"` // decomposition, when the plan is bigger than one task
	Notes    []string                `json:"notes"`    // an audit trail of what was recorded, for the human
}

var planMu sync.Mutex

func planPath(thread string) string {
	return filepath.Join(daemonDir(), "planning", thread+".json")
}

func loadDraft(thread string) *planDraft {
	planMu.Lock()
	defer planMu.Unlock()
	b, err := os.ReadFile(planPath(thread))
	if err != nil {
		return nil
	}
	var d planDraft
	if json.Unmarshal(b, &d) != nil {
		return nil // a corrupt draft is a missing draft: planning_open starts clean rather than failing
	}
	return &d
}

func saveDraft(d *planDraft) error {
	planMu.Lock()
	defer planMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(planPath(d.Thread)), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(planPath(d.Thread), b, 0o600)
}

func clearDraft(thread string) {
	planMu.Lock()
	defer planMu.Unlock()
	_ = os.Remove(planPath(thread))
}

// asTree converts the draft to the shape /work-items/tree expects.
func (d *planDraft) asTree() api.WorkTreeNode {
	kind := strings.TrimSpace(d.Kind)
	if kind == "" {
		kind = "task"
	}
	return api.WorkTreeNode{
		Kind:               kind,
		Title:              d.Title,
		Document:           d.Document,
		AcceptanceCriteria: d.Criteria,
		Children:           d.Children,
	}
}

// planStatus renders the agenda: the full checklist so the user is not trapped in an opaque
// interview, and THE NEXT UNMET SLOT with its instruction so the model knows what to do right now.
//
// Deliberately not a score. The server's own message phrases blocking checks as things to DO, and a
// model handed "3/5" will report progress instead of making some.
func planStatus(spec api.Specificity, d *planDraft) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("PLANNING MODE — draft %q (%s)\n\n", planTitleSnippet(d.Title, 60), kindOrTask(d.Kind)))

	b.WriteString("Checklist:\n")
	for _, c := range spec.Checks {
		mark := "○"
		if c.OK {
			mark = "✓"
		}
		req := ""
		if !c.Required {
			req = "  (advisory)"
		}
		b.WriteString(fmt.Sprintf("  %s %s%s\n", mark, c.ID, req))
	}

	if spec.OK {
		b.WriteString("\nEvery required slot is filled. Call planning_finalize to file this into the backlog.\n")
		b.WriteString("Before you do: confirm the plan with the user in your own words. Filing is the point of no return for their attention, not for the data.\n")
		return b.String()
	}

	// ONE slot at a time. Handing over the whole remaining list invites a model to answer them all
	// at once from its own assumptions — which is how a plan ends up specified confidently and
	// wrongly. The next question is the only question.
	next := spec.Blocking[0]
	b.WriteString("\nNEXT — " + next.ID + "\n" + next.Label + "\n")
	b.WriteString("\nAsk the user about THIS, one question, grounded in the actual repo (read files to " +
		"propose concrete targets rather than asking them to supply paths). Then record the answer " +
		"with planning_note. Do not skip ahead to the other unmet slots.\n")
	return b.String()
}

func kindOrTask(k string) string {
	if strings.TrimSpace(k) == "" {
		return "task"
	}
	return k
}

func planTitleSnippet(s string, n int) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if s == "" {
		return "untitled"
	}
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// specFor asks the CONTROL PLANE what this draft is still missing. Never computed locally — see the
// file header and CheckSpecificity.
func (s *cgServer) specFor(d *planDraft) (api.Specificity, error) {
	return s.c.CheckSpecificity(d.Title, d.Document, d.Criteria)
}

// cgNoThread is the "this repo isn't linked yet" message, shared by the prompts and by planning
// mode. One string, because two copies of a setup instruction is how half of them go stale.
const cgNoThread = "This repo isn't linked to a context thread yet. Link it once — `ptln thread bind <id>` " +
	"in the repo (create one first with `ptln thread new \"<title>\"` if needed) — then run this again: " +
	"no restart needed, this session picks the thread up immediately."

// planOpenHint tells the model what to do with a big paste.
//
// A PRD is INPUT to the mode, never a bypass of it. The obvious shortcut — "if they pasted a full
// spec, skip to filing" — hands the model the decision about whether the mode applies, which makes
// the gate advisory again and reintroduces the exact failure this design exists to fix.
//
// What a PRD legitimately does is SATISFY slots. It is written for humans, so in practice it carries
// prose and rationale but almost never a named target or a runnable acceptance check — precisely the
// two slots that predict whether the work comes back mergeable. So the interview shortens to the
// questions actually worth asking, which is also why it stops feeling like a ritual.
func planOpenHint(idea string) string {
	const long = 600 // longer than an idea, short for a spec — the threshold only changes the wording
	if len(strings.TrimSpace(idea)) < long {
		return ""
	}
	return "\nThat looks like a spec rather than a one-line idea. EXTRACT from it rather than " +
		"re-interviewing the user: pull the title, the document (targets, behaviour, guardrails) and " +
		"any acceptance criteria straight out of the text and record them with planning_note in one " +
		"call. Then the checklist will show what the spec genuinely does not cover — usually the " +
		"executable check and the exact target — and you ask only about those.\n"
}

// projectLabel is the label THIS MACHINE advertises for the repo the session is sitting in, read
// from the local daemon registry — the same registry resolveRun matches a run against.
//
// Local, not a network lookup, and deliberately so: the label has to be one a daemon will actually
// resolve to a directory. Asking the control plane could return a project this machine has never
// registered, which promotes cleanly and then fails on the box with "unknown project".
//
// Empty means "ask the user". A guess here starts real work in the wrong repo.
func (s *cgServer) projectLabel() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	root, rerr := gitwt.RepoRoot(cwd)
	if rerr != nil {
		root = cwd
	}
	for _, p := range loadDaemonRegistry().Projects {
		if pathsEqual(p.Path, root) {
			return p.Label
		}
	}
	return ""
}

// pathsEqual compares two directory paths by their resolved form, so a symlinked or trailing-slash
// registry entry still matches the cwd.
func pathsEqual(a, b string) bool {
	ra, ea := filepath.EvalSymlinks(filepath.Clean(a))
	rb, eb := filepath.EvalSymlinks(filepath.Clean(b))
	if ea != nil || eb != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// pickMachine resolves which machine runs the work, and returns a HUMAN REASON when it cannot.
//
// Auto-picking is only safe when the answer is unambiguous. With two candidates the "obvious" choice
// (first, or fastest heartbeat) is a coin flip the user did not toss — and it lands real work, in a
// real worktree, on someone's actual laptop. So: exactly one online candidate auto-picks; anything
// else asks, and says what the options were.
func (s *cgServer) pickMachine(want, label string) (daemonID, reason string) {
	peers, err := s.c.ListPeers()
	if err != nil {
		return "", "Could not list your machines: " + err.Error()
	}
	return pickFrom(peers, want, label)
}

// pickFrom is the decision, split from the fetch so it can be tested without a control plane. The
// rule is the whole point of the split: it is the difference between starting work on the right
// laptop and the wrong one.
func pickFrom(peers []api.Peer, want, label string) (daemonID, reason string) {
	want = strings.TrimSpace(want)

	var candidates []api.Peer
	for _, p := range peers {
		advertises := false
		for _, l := range p.Projects {
			if l == label {
				advertises = true
				break
			}
		}
		if !advertises {
			continue
		}
		if want != "" && !strings.EqualFold(p.DeviceLabel, want) {
			continue
		}
		candidates = append(candidates, p)
	}

	if len(candidates) == 0 {
		if want != "" {
			return "", fmt.Sprintf("No machine called %q advertises %q. Register it there with `ptln daemon add-project %s <dir>`.", want, label, label)
		}
		return "", fmt.Sprintf("No machine advertises %q yet. On the machine that has the repo: `ptln daemon add-project %s <dir>`.", label, label)
	}

	var online []api.Peer
	for _, p := range candidates {
		if p.Online {
			online = append(online, p)
		}
	}
	if len(online) == 0 {
		return "", fmt.Sprintf("%q is registered on %s, but nothing is online right now — start the daemon there and try again.",
			label, machineNames(candidates))
	}
	if len(online) > 1 {
		return "", fmt.Sprintf("More than one machine can build %q: %s. Ask the user which, then pass `machine`.",
			label, machineNames(online))
	}
	return online[0].DaemonID, online[0].DeviceLabel
}

func machineNames(ps []api.Peer) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.DeviceLabel)
	}
	return strings.Join(out, ", ")
}

// cgPlanningModeNote is appended to the SERVED persona so the CLI session knows it has a mode the
// web does not: the web planning agent talks to a party runner, this one has planning_open/note/
// finalize. The persona itself stays identical across both surfaces — only the mechanism differs.
const cgPlanningModeNote = "HOW TO RECORD IT HERE: you are in a terminal session, not the web app. " +
	"The moment this becomes real work, call `planning_open` — that enters planning mode and returns " +
	"a server-checked list of what a task needs before an agent can build it. Record every answer " +
	"with `planning_note`, and file with `planning_finalize` (it REFUSES while anything required is " +
	"missing, which is how you know the result is runnable). Then `promote_work_item` starts it on a " +
	"machine. Do not use plan_file_tree or propose_work_item while a draft is open."

// servedPersona fetches a persona from the control plane, or "" to use the embedded fallback.
func (s *cgServer) servedPersona(mode string) string {
	if s.c == nil {
		return ""
	}
	text, err := s.c.Persona(mode)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}
