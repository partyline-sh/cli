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

// planQuestion is ONE thing only the human can answer. Text is the question; Answer is what they
// said. An empty (or whitespace) Answer is UNANSWERED — the model does not get to close a question
// by asserting it is closed.
type planQuestion struct {
	Text   string `json:"text"`
	Answer string `json:"answer"`
}

func (q planQuestion) answered() bool { return strings.TrimSpace(q.Answer) != "" }

// planDraft is the work-in-progress plan. Shaped like the tree it will become, so finalize is a
// straight conversion rather than an interpretation step.
type planDraft struct {
	Thread string `json:"thread"`
	Idea   string `json:"idea"` // the opening statement or pasted PRD, kept verbatim
	// Tracker identity, when this draft began life as a ticket in someone else's system. Carried so
	// the FILED item can be stamped with it: the board links back to the original, and work_items'
	// unique (org_id, source_tool, source_id) index turns a re-import into an update instead of a
	// duplicate. Empty for a draft that started as an idea in conversation.
	SourceTool string                  `json:"source_tool,omitempty"`
	SourceID   string                  `json:"source_id,omitempty"`
	SourceURL  string                  `json:"source_url,omitempty"`
	Kind       string                  `json:"kind"` // epic | feature | task — an OUTPUT of decomposition
	Title      string                  `json:"title"`
	Document   string                  `json:"document"` // the HOW: targets, behaviour, edge cases, guardrails
	Criteria   []api.WorkItemCriterion `json:"criteria"`
	Children   []api.WorkTreeNode      `json:"children"` // decomposition, when the plan is bigger than one task
	// OpenQuestions is "I need the human", made into state. Every other slot is MECHANICAL — a model
	// can satisfy target/criteria/executable/length by reading the repo, so a product decision gets
	// silently assumed and the checklist still goes green. Answering in conversation does not help:
	// nothing records the question, nothing gates on it, and a compaction loses it. This is the one
	// slot the repo cannot fill.
	OpenQuestions []planQuestion `json:"open_questions"`
	// Assumptions do NOT block. They are the honest middle ground — the thing the model decided for
	// itself — and they are folded into the filed document so the builder and the reviewer meet them
	// as "assumed X because it was not specified", not as settled fact.
	Assumptions []string `json:"assumptions"`
}

// unanswered returns the still-open questions, in the order they were asked.
func (d *planDraft) unanswered() []planQuestion {
	var out []planQuestion
	for _, q := range d.OpenQuestions {
		if !q.answered() {
			out = append(out, q)
		}
	}
	return out
}

// addQuestion appends a question, ignoring blanks and exact repeats (a model re-recording its list
// each turn must not multiply it).
func (d *planDraft) addQuestion(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	for _, q := range d.OpenQuestions {
		if strings.EqualFold(strings.TrimSpace(q.Text), text) {
			return
		}
	}
	d.OpenQuestions = append(d.OpenQuestions, planQuestion{Text: text})
}

// answerQuestion records the human's answer. `index` is 1-based (matching what planStatus prints)
// and `match` is a case-insensitive substring of the question text; either identifies it.
//
// An empty answer is REFUSED, not stored: that is the one move that would let the model close its
// own question without the human ever speaking.
func (d *planDraft) answerQuestion(index int, match, answer string) bool {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return false
	}
	if index >= 1 && index <= len(d.OpenQuestions) {
		d.OpenQuestions[index-1].Answer = answer
		return true
	}
	match = strings.TrimSpace(strings.ToLower(match))
	if match == "" {
		return false
	}
	for i := range d.OpenQuestions {
		if strings.Contains(strings.ToLower(d.OpenQuestions[i].Text), match) {
			d.OpenQuestions[i].Answer = answer
			return true
		}
	}
	return false
}

// addAssumption appends an assumption, ignoring blanks and exact repeats.
func (d *planDraft) addAssumption(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	for _, a := range d.Assumptions {
		if strings.EqualFold(strings.TrimSpace(a), text) {
			return
		}
	}
	d.Assumptions = append(d.Assumptions, text)
}

// planUnansweredRefusal is the finalize gate for questions: "" when there is nothing outstanding, so
// a draft that asked nothing behaves exactly as it did before this existed.
func planUnansweredRefusal(d *planDraft) string {
	open := d.unanswered()
	if len(open) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Not filed — %d open question(s) the user has not answered:\n\n", len(open)))
	for _, q := range open {
		b.WriteString("  ? " + q.Text + "\n")
	}
	b.WriteString("\nAsk the user the FIRST one — in the conversation, one question — then record what " +
		"they say with planning_note (`answers`). Do not answer it yourself: if it were yours to " +
		"decide it was never an open question, and what you decided belongs in `assumptions`.\n")
	return b.String()
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
		Document:           documentWithAssumptions(d.Document, d.Assumptions),
		AcceptanceCriteria: d.Criteria,
		Children:           d.Children,
	}
}

// documentWithAssumptions appends what the plan ASSUMED rather than established, under its own
// heading. It goes into the filed document (not the draft's) so the specificity gate keeps judging
// the text the human actually wrote — but the builder and the reviewer both open the item and see
// "assumed X because it was not specified" instead of meeting X as a settled requirement.
func documentWithAssumptions(doc string, assumptions []string) string {
	if len(assumptions) == 0 {
		return doc
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(doc, "\n"))
	if strings.TrimSpace(doc) != "" {
		b.WriteString("\n\n")
	}
	b.WriteString("## Assumptions\n\nNot specified by the requester — assumed while planning. " +
		"Treat each as a decision to check, not a requirement that was given:\n\n")
	for _, a := range assumptions {
		b.WriteString("- " + strings.TrimSpace(a) + "\n")
	}
	return b.String()
}

// ----- the RECORD: what a terminal planning session leaves behind -----
//
// A plan filed from a terminal used to be invisible: work_items render only through a run, so the
// CLI wrote correct rows nobody could open. A web planning session has no such problem because it
// IS a party — /p/[id] is its home, it shows in History and on the Dashboard, and the filed tree
// points back at it. So the CLI files against the SAME machinery: at finalize it creates a party
// whose DOCUMENT is this record, and files the tree with origin_party_id pointing at it.
//
// WHAT THIS IS NOT: a transcript. The conversation happened in someone's terminal and we do not
// have it — writing party_messages that read like the session would be a fabrication, and a
// reconstruction presented as a transcript is a lie whatever its accuracy. What we DO have is the
// draft: the idea verbatim, every question that was put to the human with what they answered, and
// everything the model decided on its own. That is a decision log, and it is labelled as one.

// planRecordDocument renders the party document for a filed plan: the specification, then how it
// came to be specified. Safe on an empty draft — every section is skipped when it has no content,
// so a one-line task with no questions and no assumptions still yields a valid document.
func planRecordDocument(d *planDraft) string {
	var b strings.Builder
	b.WriteString("# " + planTitleSnippet(d.Title, 120) + "\n\n")
	b.WriteString("_Planned in a terminal session (" + kindOrTask(d.Kind) + "). Filed from the CLI._\n\n")

	b.WriteString("## Specification\n\n")
	if doc := strings.TrimSpace(d.Document); doc != "" {
		b.WriteString(doc + "\n\n")
	} else {
		b.WriteString("_No document was recorded._\n\n")
	}

	if len(d.Criteria) > 0 {
		b.WriteString("## Acceptance criteria\n\n")
		for _, c := range d.Criteria {
			verify := strings.TrimSpace(c.Verify)
			if verify == "" {
				verify = "unspecified"
			}
			b.WriteString("- " + strings.TrimSpace(c.Text) + " _(" + verify + ")_\n")
		}
		b.WriteString("\n")
	}

	if len(d.Children) > 0 {
		b.WriteString("## Decomposition\n\n")
		writePlanChildren(&b, d.Children, 0)
		b.WriteString("\n")
	}

	b.WriteString(planDecisionLog(d))
	return b.String()
}

func writePlanChildren(b *strings.Builder, nodes []api.WorkTreeNode, depth int) {
	for _, n := range nodes {
		b.WriteString(strings.Repeat("  ", depth) + "- **" + kindOrTask(n.Kind) + "** — " + strings.TrimSpace(n.Title) + "\n")
		writePlanChildren(b, n.Children, depth+1)
	}
}

// planDecisionLog is the "How this was specified" section: the idea as the user stated it, each
// question WITH the answer that closed it, and each assumption marked as assumed rather than asked.
// Always present, even when there is nothing but the idea — the heading is the honest statement
// that this is a decision log and not a transcript.
func planDecisionLog(d *planDraft) string {
	var b strings.Builder
	b.WriteString("## How this was specified\n\n")
	b.WriteString("_A decision log, not a transcript — the conversation happened in a terminal and is " +
		"not recorded. What follows is what the draft carried when it was filed._\n\n")

	b.WriteString("### The idea, as stated\n\n")
	if idea := strings.TrimSpace(d.Idea); idea != "" {
		b.WriteString(idea + "\n\n")
	} else {
		b.WriteString("_No opening statement was recorded._\n\n")
	}

	if len(d.OpenQuestions) > 0 {
		b.WriteString("### Questions asked, and answered\n\n")
		for _, q := range d.OpenQuestions {
			b.WriteString("- **Q:** " + strings.TrimSpace(q.Text) + "\n")
			answer := strings.TrimSpace(q.Answer)
			if answer == "" {
				// Unreachable via finalize (the gate refuses first), but a record that quietly dropped
				// an unanswered question would be the same silent failure the gate exists to stop.
				answer = "_unanswered_"
			}
			b.WriteString("  **A:** " + answer + "\n")
		}
		b.WriteString("\n")
	}

	if len(d.Assumptions) > 0 {
		b.WriteString("### Assumed, not asked\n\n")
		b.WriteString("_Decided while planning because it was not specified. Treat each as a decision to " +
			"check, not a requirement that was given._\n\n")
		for _, a := range d.Assumptions {
			b.WriteString("- assumed: " + strings.TrimSpace(a) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// planRecordMode is the party mode a filed CLI plan lives in. Not pickable in the web's Start-a-
// party picker (see web/src/lib/api/party-modes.ts) — nobody starts one of these by hand; it is a
// record a finalize writes. It is NOT excluded from History or the Dashboard, which is the entire
// point: unlike project_setup, this has no other home to be found in.
const planRecordMode = "plan"

// createPlanRecord creates the party the plan will be readable in and writes the record into its
// document. Returns the party id (for origin_party_id) and the URL to read it at.
//
// Called ONCE, at finalize, and its error is fatal to filing — see the caller. Nothing is posted to
// the party's channel: there is no transcript to post.
func (s *cgServer) createPlanRecord(d *planDraft) (partyID, url string, err error) {
	if s.c == nil {
		return "", "", fmt.Errorf("not signed in to a control plane")
	}
	org, oerr := s.c.DefaultOrgSlug()
	if oerr != nil {
		return "", "", fmt.Errorf("could not resolve your team: %w", oerr)
	}
	if strings.TrimSpace(org) == "" {
		return "", "", fmt.Errorf("no team found for your account — sign in with `ptln login`")
	}
	p, perr := s.c.CreatePartyWithDoc(org, planRecordMode, planRecordDocument(d))
	if perr != nil {
		return "", "", perr
	}
	// The client's own base, not api.Base(): a session pointed at staging must be told where IT
	// filed, not where production lives.
	base := strings.TrimRight(strings.TrimSpace(s.c.Base), "/")
	if base == "" {
		base = api.Base()
	}
	return p.ID, base + "/p/" + p.ID, nil
}

// planStatus renders the agenda: the full checklist so the user is not trapped in an opaque
// interview, and THE NEXT UNMET SLOT with its instruction so the model knows what to do right now.
//
// Deliberately not a score. The server's own message phrases blocking checks as things to DO, and a
// model handed "3/5" will report progress instead of making some.
// Open questions come FIRST, above the checklist and ahead of any mechanical slot, because every
// other slot can be satisfied by reading the repo. Ordering them last would mean the interview asks
// the human for paths it could have looked up and assumes the decisions only they can make.
func planStatus(spec api.Specificity, d *planDraft) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("PLANNING MODE — draft %q (%s)\n\n", planTitleSnippet(d.Title, 60), kindOrTask(d.Kind)))

	open := d.unanswered()
	if len(d.OpenQuestions) > 0 {
		b.WriteString(fmt.Sprintf("Open questions for the user (%d unanswered):\n", len(open)))
		for i, q := range d.OpenQuestions {
			if q.answered() {
				b.WriteString(fmt.Sprintf("  ✓ %d. %s — %s\n", i+1, q.Text, q.Answer))
				continue
			}
			b.WriteString(fmt.Sprintf("  ? %d. %s\n", i+1, q.Text))
		}
		b.WriteString("\n")
	}
	if len(d.Assumptions) > 0 {
		b.WriteString("Assumptions (filed with the plan, not blocking):\n")
		for _, a := range d.Assumptions {
			b.WriteString("  · " + a + "\n")
		}
		b.WriteString("\n")
	}

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

	// ONE next thing, questions first. An unanswered question outranks every mechanical slot AND the
	// exit: a green checklist on top of an unasked product decision is exactly the silent failure
	// this gate exists to stop.
	if len(open) > 0 {
		b.WriteString("\nNEXT — open question\n" + open[0].Text + "\n")
		b.WriteString("\nAsk the user THIS, in the conversation, and record their answer with " +
			"planning_note (`answers`). Only they can settle it — do not read the repo and decide it " +
			"for them, and do not move on to the checklist until it is answered. planning_finalize " +
			"refuses while it is open.\n")
		return b.String()
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
	"a server-checked list of what a task needs before an agent can build it. FIRST record what you " +
	"must ASK, not what you know: the checklist's slots can all be satisfied by reading the repo, so " +
	"the decisions that are genuinely the user's go as `open_questions` on planning_note, and " +
	"anything you decide for yourself goes as `assumptions` rather than being stated as fact. Record " +
	"every answer with `planning_note`, and file with `planning_finalize` (it REFUSES while a " +
	"question is unanswered or anything required is missing, which is how you know the result is " +
	"runnable). Then `promote_work_item` starts it on a machine. Do not use plan_file_tree or " +
	"propose_work_item while a draft is open."

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

// isContainer decides promote-tree vs promote. A container's LEAVES are what run; the container
// itself never does — getting this backwards enqueues a whole epic as one unbuildable task, or
// promotes a lone task as an empty tree.
func isContainer(root api.WorkTreeNode) bool { return len(root.Children) > 0 }
