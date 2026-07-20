package main

// ptln describe — the Requirements Agent, terminal edition. Turns a vague idea into a well-specified,
// SCORED backlog item (an Epic, Feature, or Task) and records it into a thread's planning tree. It
// runs entirely on YOUR machine through YOUR OWN `claude` binary and auth (never a server-side key —
// same posture as crank/party/verify), using the current repo for code-anchored questions. On accept
// it POSTs /api/v1/work-items (the Slice 1 planning layer) — so the item shows in the /work/plan tree
// and, if it's a task, can be Started from the board. The in-session twin is the `/describe` MCP
// slash command (cg-mcp); this is the standalone command for when you're NOT already in an agent.
//
// Two modes, switchable MID-INTERVIEW (some ideas are simple, some complex):
//   • deep  (default) — one focused question at a time.
//   • quick           — ask every remaining question at once, then emit.
// Type /quick or /deep to switch, /done to emit now with best effort, /quit to abort.

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
)

var jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*\\})\\s*```")

type reqCriterion struct {
	Text   string `json:"text"`
	Verify string `json:"verify"` // executable check | adversarial review | behavior review
}

type reqWorkitem struct {
	Kind               string         `json:"kind"` // agent-chosen granularity (one-shot mode): epic|feature|task
	Title              string         `json:"title"`
	Document           string         `json:"document"` // the HOW: constraints, edge cases, approach
	AcceptanceCriteria []reqCriterion `json:"acceptance_criteria"`
}

// reqItem is the agent's structured turn output: a readiness score + assessment, plus EITHER
// questions (not ready) OR a workitem (ready). The CLI, not the model, decides when to record.
// Used by the INTERACTIVE interview path (describeMain).
type reqItem struct {
	Ready      bool         `json:"ready"`
	Readiness  int          `json:"readiness"` // 0–5
	Assessment string       `json:"assessment"`
	Questions  []string     `json:"questions"`
	Workitem   *reqWorkitem `json:"workitem"`
}

// reqNode is one node of a DECOMPOSITION (Part 1, one-shot path): a work item plus its children,
// recursively. Kinds are agent-decided (epic▸feature▸task); a TASK LEAF (no children) is what
// actually runs, so it must be independently buildable and fit the run cap.
type reqNode struct {
	Kind               string         `json:"kind"`
	Title              string         `json:"title"`
	Document           string         `json:"document"`
	AcceptanceCriteria []reqCriterion `json:"acceptance_criteria"`
	Readiness          int            `json:"readiness"`
	Children           []reqNode      `json:"children"`
}

// reqTree is the one-shot HEADLESS turn output: an overall readiness + the ROOT of the decomposition
// tree (a single task when the idea is small; an epic/feature with children when it's larger).
type reqTree struct {
	Ready      bool     `json:"ready"`
	Readiness  int      `json:"readiness"`
	Assessment string   `json:"assessment"`
	Root       *reqNode `json:"root"`
}

func describeMain(args []string) {
	fs := flag.NewFlagSet("describe", flag.ExitOnError)
	threadFlag := fs.String("thread", "", "thread id to file the item under (default: prompt to choose)")
	kindFlag := fs.String("kind", "task", "work item kind: epic | feature | task")
	quick := fs.Bool("quick", false, "start in quick mode (ask everything at once)")
	timeout := fs.Duration("timeout", 4*time.Minute, "per-turn engine timeout")
	_ = fs.Parse(args)

	kind := strings.ToLower(strings.TrimSpace(*kindFlag))
	if kind != "epic" && kind != "feature" && kind != "task" {
		fatal(fmt.Errorf("--kind must be epic, feature, or task"))
	}

	client := api.New()
	if client.Token == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login` first"))
	}
	threadID := resolveDescribeThread(client, *threadFlag)
	dir, _ := os.Getwd() // the interview runs here so the agent can ground questions in this repo

	mode := "deep"
	if *quick {
		mode = "quick"
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)

	fmt.Printf("\n◆ describe — Requirements Agent (%s)\n", kind)
	fmt.Printf("  runs your local %s · mode: %s (/quick /deep to switch · /done · /quit)\n\n", engineLabel(engineForCwd()), mode)
	fmt.Print("What do you want to build?\n> ")
	if !in.Scan() {
		return
	}
	first := strings.TrimSpace(in.Text())
	if first == "" {
		fatal(fmt.Errorf("nothing to describe"))
	}

	// Turn 1 carries the full instruction + the idea. Later turns keep context via --resume on
	// claude; engines without headless resume carry the whole transcript each turn instead
	// (stateless — same interview, more tokens). Engine = this project's registered one (cwd).
	sess := newInterviewSession(dir, engineForCwd(), *timeout)
	prompt := interviewPreamble(mode, kind) + "\n\nThe idea:\n" + first

	for {
		reply, err := sess.turn(prompt)
		if err != nil {
			fatal(fmt.Errorf("engine turn failed: %w", err))
		}
		item, ok := parseReqItem(reply)
		if !ok {
			fmt.Printf("\n%s\n", strings.TrimSpace(reply))
			fmt.Print("\n> ")
			if !in.Scan() {
				return
			}
			prompt = strings.TrimSpace(in.Text())
			continue
		}

		if item.Workitem != nil && item.Ready {
			if confirmAndCreate(client, threadID, kind, item, in) {
				return
			}
			fmt.Print("\nWhat should change?\n> ")
			if !in.Scan() {
				return
			}
			prompt = strings.TrimSpace(in.Text())
			continue
		}

		printAssessment(item)
		fmt.Print("> ")
		if !in.Scan() {
			return
		}
		line := strings.TrimSpace(in.Text())
		switch line {
		case "/quit":
			fmt.Println("aborted — nothing recorded")
			return
		case "/quick":
			mode = "quick"
			prompt = "(Switch to QUICK mode: ask every remaining question at once, then be ready to emit the work item.)"
		case "/deep":
			mode = "deep"
			prompt = "(Switch to DEEP mode: ask one focused question at a time.)"
		case "/done":
			prompt = "(Emit the work item NOW with your best assessment, even if some details are unconfirmed. Set ready=true.)"
		default:
			prompt = line
		}
	}
}

// projectLabelForCwd maps the working directory to a locally-registered project label (the daemon
// registry's Path is an ancestor of cwd). Longest-path match wins so nested registrations resolve to
// the most specific project. "" when cwd is inside no registered project.
func projectLabelForCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	best := ""
	bestLen := 0
	for _, p := range loadDaemonRegistry().Projects {
		if p.Path == "" {
			continue
		}
		rel, err := filepath.Rel(p.Path, cwd)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(p.Path) > bestLen {
			best, bestLen = p.Label, len(p.Path)
		}
	}
	return best
}

// resolveDescribeThread returns the thread to file the item under: the flag if given, the sole thread
// if there's exactly one, else a numbered pick.
func resolveDescribeThread(client *api.Client, flagID string) string {
	if flagID != "" {
		return flagID
	}
	// projects⊃threads: infer the project from cwd (local registry) and let the SERVER resolve its
	// thread — created lazily, umbrella-walked — so `ptln shape` in a repo needs no flags at all.
	if label := projectLabelForCwd(); label != "" {
		if t, err := client.ResolveThread(label); err == nil && t != nil {
			fmt.Printf("planning into %s (project %s)\n", t.Title, label)
			return t.ID
		}
	}
	threads, err := client.ListThreads()
	if err != nil {
		fatal(fmt.Errorf("couldn't list threads: %w", err))
	}
	if len(threads) == 0 {
		fatal(fmt.Errorf("no threads — create one in the web app (or `ptln thread new`) first"))
	}
	if len(threads) == 1 {
		return threads[0].ID
	}
	fmt.Println("\nFile this item under which thread?")
	for i, t := range threads {
		fmt.Printf("  %d) %s\n", i+1, t.Title)
	}
	fmt.Print("> ")
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		fatal(fmt.Errorf("no selection"))
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(sc.Text()), "%d", &n); err != nil || n < 1 || n > len(threads) {
		fatal(fmt.Errorf("invalid selection"))
	}
	return threads[n-1].ID
}

// interviewSession keeps one interview's cross-turn context, per engine capability: claude
// carries it via --resume (the session handle from each turn's JSON envelope); engines without
// headless resume get the WHOLE conversation re-sent each turn (stateless). Either way the
// interview reads the same to the user.
type interviewSession struct {
	dir     string
	engine  string
	spec    eng.Spec
	resume  string
	convo   strings.Builder
	timeout time.Duration
}

func newInterviewSession(dir, engineName string, timeout time.Duration) *interviewSession {
	spec, _ := engineSpecFor(engineName) // engineForCwd only returns registry-valid names
	return &interviewSession{dir: dir, engine: engineName, spec: spec, timeout: timeout}
}

func (s *interviewSession) turn(prompt string) (string, error) {
	if s.spec.Caps.Resume {
		reply, handle, err := runInterviewTurn(s.dir, s.engine, prompt, s.resume, s.timeout)
		if handle != "" {
			s.resume = handle
		}
		return reply, err
	}
	s.convo.WriteString("USER: " + prompt + "\n")
	reply, _, err := runInterviewTurn(s.dir, s.engine, s.convo.String(), "", s.timeout)
	if err == nil {
		s.convo.WriteString("AGENT: " + reply + "\n")
	}
	return reply, err
}

// runInterviewTurn runs one engine turn LOCALLY (the user's binary + auth), in `dir` so the agent can
// ground questions in the repo. Read-only tools only — the interview never edits the repo. Claude
// keeps its exact pre-adapter argv (--output-format json + --resume, parsed for the session handle);
// other engines go through the adapter's one-shot read-only posture and return no handle (the
// interviewSession carries the transcript instead). Antigravity can't enforce read-only headless and
// errors here, fail closed.
func runInterviewTurn(dir, engineName, prompt, resume string, timeout time.Duration) (string, string, error) {
	spec, ok := engineSpecFor(engineName)
	if !ok {
		return "", "", fmt.Errorf("unknown engine %q", engineName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if spec.Name != "claude" {
		argv, stdinPrompt, err := spec.OneShotArgs(prompt, "", eng.ToolsReadOnly)
		if err != nil {
			return "", "", err
		}
		out, err := runOneShot(ctx, dir, argv, stdinPrompt)
		if ctx.Err() == context.DeadlineExceeded {
			return "", "", fmt.Errorf("timed out (>%s)", timeout)
		}
		if err != nil {
			return "", "", err
		}
		return oneShotText(spec, out), "", nil
	}
	cargs := []string{"-p", prompt, "--output-format", "json", "--allowedTools", "Read Grep Glob"}
	if resume != "" {
		cargs = append(cargs, "--resume", resume)
	}
	cmd := exec.CommandContext(ctx, "claude", cargs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PARTYLINE=1")
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", "", fmt.Errorf("timed out (>%s)", timeout)
	}
	if err != nil {
		return "", "", err
	}
	reply, _, handle, ok := parseWorkerOutput(out)
	if !ok {
		reply = string(out)
	}
	return reply, handle, nil
}

// parseReqItem extracts the agent's JSON turn output, tolerating prose around a fenced ```json block
// (preferred) or a bare object. Returns ok=false when nothing parses so the loop can nudge the model.
func parseReqItem(reply string) (reqItem, bool) {
	var raw string
	if m := jsonBlockRe.FindStringSubmatch(reply); m != nil {
		raw = m[1]
	} else if i := strings.Index(reply, "{"); i >= 0 {
		if j := strings.LastIndex(reply, "}"); j > i {
			raw = reply[i : j+1]
		}
	}
	if raw == "" {
		return reqItem{}, false
	}
	var item reqItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return reqItem{}, false
	}
	return item, true
}

// parseReqTree extracts the one-shot DECOMPOSITION turn output (same tolerant fenced-block/bare-object
// handling as parseReqItem, into reqTree). ok=false when nothing parses.
func parseReqTree(reply string) (reqTree, bool) {
	var raw string
	if m := jsonBlockRe.FindStringSubmatch(reply); m != nil {
		raw = m[1]
	} else if i := strings.Index(reply, "{"); i >= 0 {
		if j := strings.LastIndex(reply, "}"); j > i {
			raw = reply[i : j+1]
		}
	}
	if raw == "" {
		return reqTree{}, false
	}
	var tree reqTree
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		return reqTree{}, false
	}
	return tree, true
}

// toWorkTreeNode converts the agent's reqNode into the API's WorkTreeNode recursively. Kinds are
// passed through trimmed/lowercased — an invalid kind is left for the server to reject with a clear
// message (never silently defaulted, which could make an illegal nesting).
func toWorkTreeNode(n *reqNode) api.WorkTreeNode {
	crit := make([]api.WorkItemCriterion, 0, len(n.AcceptanceCriteria))
	for _, c := range n.AcceptanceCriteria {
		crit = append(crit, api.WorkItemCriterion{Text: strings.TrimSpace(c.Text), Verify: strings.TrimSpace(c.Verify)})
	}
	kids := make([]api.WorkTreeNode, 0, len(n.Children))
	for i := range n.Children {
		kids = append(kids, toWorkTreeNode(&n.Children[i]))
	}
	return api.WorkTreeNode{
		Kind:               strings.ToLower(strings.TrimSpace(n.Kind)),
		Title:              strings.TrimSpace(n.Title),
		Document:           strings.TrimSpace(n.Document),
		AcceptanceCriteria: crit,
		Readiness:          n.Readiness,
		Children:           kids,
	}
}

// countNodes returns the total nodes in a decomposition (for the run summary).
func countNodes(n *api.WorkTreeNode) int {
	total := 1
	for i := range n.Children {
		total += countNodes(&n.Children[i])
	}
	return total
}

func printAssessment(item reqItem) {
	fmt.Printf("\n  readiness %d/5 — %s\n", item.Readiness, strings.TrimSpace(item.Assessment))
	if len(item.Questions) == 0 {
		fmt.Println("  (answer freely, or /done to emit now)")
		return
	}
	fmt.Println()
	for _, q := range item.Questions {
		fmt.Printf("  • %s\n", strings.TrimSpace(q))
	}
	fmt.Println()
}

// confirmAndCreate renders the scored work item, asks for a y/N, and on yes records it into the
// planning tree via /api/v1/work-items. Returns true when recorded (or the flow aborted).
func confirmAndCreate(client *api.Client, threadID, kind string, item reqItem, in *bufio.Scanner) bool {
	wi := item.Workitem
	fmt.Printf("\n──────── %s (readiness %d/5) ────────\n", kind, item.Readiness)
	fmt.Printf("  %s\n", strings.TrimSpace(wi.Title))
	if strings.TrimSpace(wi.Document) != "" {
		fmt.Printf("\n%s\n", strings.TrimSpace(wi.Document))
	}
	if len(wi.AcceptanceCriteria) > 0 {
		fmt.Println("\n  acceptance criteria:")
		for _, c := range wi.AcceptanceCriteria {
			fmt.Printf("    - [%s] %s\n", strings.TrimSpace(c.Verify), strings.TrimSpace(c.Text))
		}
	}
	fmt.Print("\nRecord this to the backlog? [y/N] ")
	if !in.Scan() {
		return true
	}
	if a := strings.ToLower(strings.TrimSpace(in.Text())); a != "y" && a != "yes" {
		return false
	}

	criteria := make([]api.WorkItemCriterion, 0, len(wi.AcceptanceCriteria))
	for _, c := range wi.AcceptanceCriteria {
		criteria = append(criteria, api.WorkItemCriterion{Text: c.Text, Verify: c.Verify})
	}
	id, err := client.CreateWorkItem(threadID, kind, strings.TrimSpace(wi.Title), "", strings.TrimSpace(wi.Document), item.Readiness, criteria)
	if err != nil {
		fatal(fmt.Errorf("record failed: %w", err))
	}
	fmt.Printf("\n✓ recorded %s → backlog (id %s) · see it in /work/plan\n", kind, id)
	return true
}

// runDescribeToWorkItem is the ONE-SHOT, headless path — the daemon's describe job (preset "describe")
// calls this when the web submits an idea for async processing. No interview loop: a single local engine
// turn turns the idea into its best-specified work item and the agent PICKS the granularity itself
// (epic|feature|task). Records it into the planning tree via the user's own token (the daemon runs on
// the user's machine, so api.New() loads ~/.partyline/token). onLog (when non-nil) receives a
// human-readable line per step as the agent works, so the web tails it live (same as a crank run).
// `model` is the project's plan model for the turn (already modelRe-validated by the caller —
// see runDescribeJob); empty runs the engine's own default.
// Returns a JSON summary (for the web to render the outcome + link) or an error.
func runDescribeToWorkItem(client *api.Client, threadID, dir, engineName, idea, globals, model string, onLog func(string), timeout time.Duration) (string, error) {
	tree, err := runDecomposeStreaming(dir, engineName, idea, globals, model, onLog, timeout)
	if err != nil {
		return "", err
	}
	root := toWorkTreeNode(tree.Root)
	if root.Readiness == 0 {
		root.Readiness = tree.Readiness // fall back to the overall score for the root
	}
	if onLog != nil {
		onLog(fmt.Sprintf("✓ decomposed into a %s %q (%d item(s)) — recording", root.Kind, root.Title, countNodes(&root)))
	}
	rootID, count, err := client.CreateWorkTree(threadID, root)
	if err != nil {
		// The server's error is actionable (e.g. names an over-large task leaf) — surface it as-is.
		return "", fmt.Errorf("record failed: %w", err)
	}
	// Structured summary so the web renders the outcome (kind/title/count) + a link. Stored on the run.
	b, _ := json.Marshal(map[string]any{"work_item_id": rootID, "kind": root.Kind, "title": root.Title, "readiness": root.Readiness, "count": count})
	return string(b), nil
}

// runDecomposeStreaming runs the one-shot DECOMPOSE turn in STREAMING mode (claude --output-format
// stream-json --verbose), humanizing each event to onLog as it works — the SAME live-output path crank
// runs use. Read-only tools; no bash, no MCP. Parses the terminal result into a decomposition tree.
// `globals` (the project document) is folded into the prompt as the stack/guardrail constraints that
// define "buildable". `model` (pre-validated, see runDescribeToWorkItem) picks the claude model;
// empty = the host default. onLog may be nil (a quiet stream with no sink).
func runDecomposeStreaming(dir, engineName, idea, globals, model string, onLog func(string), timeout time.Duration) (reqTree, error) {
	spec, okSpec := engineSpecFor(engineName)
	if !okSpec {
		return reqTree{}, fmt.Errorf("unknown engine %q", engineName)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	prompt := describeOneShotPreamble(globals) + "\n\nThe idea:\n" + idea
	sink := onLog
	if sink == nil {
		sink = func(string) {}
	}
	// Claude streams (the SAME live-output path crank runs use). Engines without a parseable
	// stream run buffered via the adapter — same read-only posture, no live deltas; antigravity
	// can't enforce read-only headless and errors here (fail closed, never weaker than asked).
	if !spec.Stream {
		argv, stdinPrompt, err := spec.OneShotArgs(prompt, model, eng.ToolsReadOnly)
		if err != nil {
			return reqTree{}, err
		}
		sink(fmt.Sprintf("engine %s does not stream; buffering output…", spec.Name))
		out, err := runOneShot(ctx, dir, argv, stdinPrompt)
		if ctx.Err() == context.DeadlineExceeded {
			return reqTree{}, fmt.Errorf("engine turn timed out (>%s)", timeout)
		}
		if err != nil {
			return reqTree{}, fmt.Errorf("engine turn failed (%s)", spec.Name)
		}
		tree, ok := parseReqTree(oneShotText(spec, out))
		if !ok || tree.Root == nil {
			return reqTree{}, fmt.Errorf("agent did not produce a decomposition")
		}
		return tree, nil
	}
	cargs := []string{"-p", prompt, "--output-format", "stream-json", "--verbose", "--allowedTools", "Read Grep Glob"}
	if model != "" {
		cargs = append(cargs, "--model", model)
	}
	cmd := exec.CommandContext(ctx, "claude", cargs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PARTYLINE=1")
	outcome, err := runWorkerStreaming(ctx, cmd, timeout, sink)
	if err != nil {
		return reqTree{}, fmt.Errorf("engine turn failed: %w", err)
	}
	tree, ok := parseReqTree(outcome.text)
	if !ok || tree.Root == nil {
		return reqTree{}, fmt.Errorf("agent did not produce a decomposition")
	}
	return tree, nil
}

// describeOneShotPreamble is the instruction for the headless one-shot path. Same PM+tech-lead persona
// as the live `describe` party mode (party-modes.ts) — outcome-first, solution-oriented — but headless
// (no interview): it DECOMPOSES the idea into a nested plan whose TASK LEAVES are each independently
// buildable and small enough to run, choosing kinds and structure itself. `globals` (the project
// document) folds in as the stack/conventions/guardrails that define "buildable" (empty when none).
func describeOneShotPreamble(globals string) string {
	p := `You are a PRODUCT-MINDED TECH LEAD turning a rough idea into a buildable plan for a backlog —
part product manager (pin the outcome, pick a sensible scope), part architect (propose HOW, cut runnable
tasks). You are running HEADLESS — there is NO user to ask, so do NOT ask questions; produce your best
plan in one pass. Use the current repository to ground everything in the real code (read files as needed).

WORK OUTCOME-FIRST. Start from what should be TRUE when this is done — the observable change and the
deliverables — and make the plan deliver exactly that. Capture that outcome in "assessment".

PROPOSE SOLUTIONS, DON'T REPORT GAPS. Read the repo to DESIGN the change, not to dead-end. If something
doesn't exist yet, that IS the thing to build: specify HOW (which files/layers, how it integrates),
citing what's there as the foundation you extend — never let "this doesn't exist" be a conclusion.
Separate what the code does TODAY from the DESIRED future state you're specifying: the repo may be the
same product you're planning a feature for, so don't conflate "it can't do X yet" with "X can't be built".

DECOMPOSE BY SIZE:
  • If the idea is one small, agent-buildable piece → the plan is a single "task" (no children).
  • If it's larger → a "feature" whose children are the tasks that build it, or an "epic" whose children
    are features, each with task children. Nesting is strictly epic▸feature▸task.
  • CHOOSE the kinds and structure yourself — the user does not pick them.

TASK LEAVES ARE THE UNIT THAT RUNS. Each one is built by a SEPARATE autonomous agent that starts from a
fresh checkout of the main branch and does NOT see the other tasks' code. So:
  • Cut INDEPENDENT, vertically-sliced tasks that can each be built and merged on their own.
  • Where work is genuinely sequential (step B needs step A's code), DO NOT split it into separate tasks
    that depend on each other — keep it as ONE task, because a later task can't build on an earlier one.
  • Keep every task SMALL: its title + document + acceptance criteria together must be well under 3500
    characters. If a task would be bigger, split it into independent tasks or lift detail to the parent.

A to-do list says WHAT; a well-specified task must say HOW — constraints, edge cases, explicit
NON-GOALS (what is out of scope), and a concrete definition of done. Write the "document" as a real
mini-spec: the approach, the non-goals, and any assumptions or OPEN QUESTIONS you had to make — surface
an open question rather than inventing scope the user didn't ask for.

MAKE ACCEPTANCE CRITERIA TESTABLE. Each criterion must be a specific, observable outcome someone could
check — never a vague aspiration ("works well"). This is the definition of done the builder builds to
AND the reviewer verifies each one against, so choose "verify" honestly:
  • "executable check" ONLY when a command/test can prove it (build passes, this test is green).
  • "behavior review" for an observable behavior a reviewer confirms from the diff.
  • "adversarial review" for correctness/quality judgments with no mechanical check.
Score readiness 0–5 (5 = an agent could build it with no further clarification).

Respond with ONE fenced ` + "```json" + ` block, and nothing else, of this exact shape (children is
optional/empty for a leaf):
{
  "ready": true,
  "readiness": <0-5>,
  "assessment": "<one line: the shape of the plan / any uncertainty>",
  "root": {
    "kind": "epic|feature|task",
    "title": "<concise imperative title>",
    "document": "<the HOW: constraints, edge cases, approach>",
    "acceptance_criteria": [
      {"text": "<verifiable criterion>", "verify": "executable check|adversarial review|behavior review"}
    ],
    "children": [ { "kind": "...", "title": "...", "document": "...", "acceptance_criteria": [], "children": [] } ]
  }
}`
	if strings.TrimSpace(globals) != "" {
		p += "\n\nPROJECT GLOBALS — the stack, conventions, and guardrails every task must respect (treat as authoritative):\n" + strings.TrimSpace(globals)
	}
	return p
}

// interviewPreamble is the requirements-analyst instruction sent on turn 1 (later turns inherit it via
// --resume). It pins the OUTPUT CONTRACT (one JSON turn object, always) and the philosophy: a task for
// an agent must say HOW, not just WHAT — clarity before execution.
func interviewPreamble(mode, kind string) string {
	pace := "Ask ONE focused question at a time."
	if mode == "quick" {
		pace = "Ask EVERY remaining question at once in a single list."
	}
	scope := map[string]string{
		"epic":    "an EPIC (a large effort — the document is a brief/PRD).",
		"feature": "a FEATURE (one shippable unit).",
		"task":    "a single TASK (one agent-buildable piece with a concrete definition of done).",
	}[kind]
	return `You are a requirements analyst helping turn a rough idea into ` + scope + ` for our backlog.
A to-do list says WHAT; a well-specified item must say HOW — the constraints, edge cases, and a
concrete definition of done. Use the current repository to ground your questions in the real code
(read files as needed). ` + pace + `

Score readiness 0–5: 5 = an autonomous agent could build this correctly with no further clarification.
Do NOT invent scope the user didn't ask for.

Respond EVERY turn with ONE fenced ` + "```json" + ` block, and nothing else, of this exact shape:
{
  "ready": <bool>,
  "readiness": <0-5>,
  "assessment": "<one line: what's still unclear, or why it's ready>",
  "questions": ["<clarifying question>", ...],   // [] when ready
  "workitem": {                                   // null until ready
    "title": "<concise imperative title>",
    "document": "<the HOW: constraints, edge cases, approach — the spec body>",
    "acceptance_criteria": [
      {"text": "<verifiable criterion>", "verify": "executable check|adversarial review|behavior review"}
    ]
  }
}
Set ready=true and fill workitem ONLY when readiness is high; otherwise keep workitem null and ask.`
}
