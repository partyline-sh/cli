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
	"regexp"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
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
type reqItem struct {
	Ready      bool         `json:"ready"`
	Readiness  int          `json:"readiness"` // 0–5
	Assessment string       `json:"assessment"`
	Questions  []string     `json:"questions"`
	Workitem   *reqWorkitem `json:"workitem"`
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
	fmt.Printf("  runs your local claude · mode: %s (/quick /deep to switch · /done · /quit)\n\n", mode)
	fmt.Print("What do you want to build?\n> ")
	if !in.Scan() {
		return
	}
	first := strings.TrimSpace(in.Text())
	if first == "" {
		fatal(fmt.Errorf("nothing to describe"))
	}

	// Turn 1 carries the full instruction + the idea; later turns rely on --resume for context.
	prompt := interviewPreamble(mode, kind) + "\n\nThe idea:\n" + first
	resume := ""

	for {
		reply, sess, err := runInterviewTurn(dir, prompt, resume, *timeout)
		if err != nil {
			fatal(fmt.Errorf("engine turn failed: %w", err))
		}
		if sess != "" {
			resume = sess
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

// resolveDescribeThread returns the thread to file the item under: the flag if given, the sole thread
// if there's exactly one, else a numbered pick.
func resolveDescribeThread(client *api.Client, flagID string) string {
	if flagID != "" {
		return flagID
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

// runInterviewTurn runs one claude turn LOCALLY (the user's binary + auth), in `dir` so the agent can
// ground questions in the repo. Read-only tools only — the interview never edits the repo. Reuses
// parseWorkerOutput to pull (reply, resume-handle) from claude's --output-format json envelope.
func runInterviewTurn(dir, prompt, resume string, timeout time.Duration) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
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
// calls this when the web submits an idea for async processing. No interview loop: a single local claude
// turn turns the idea into its best-specified work item and the agent PICKS the granularity itself
// (epic|feature|task). Records it into the planning tree via the user's own token (the daemon runs on
// the user's machine, so api.New() loads ~/.partyline/token). Returns a one-line summary or an error.
func runDescribeToWorkItem(client *api.Client, threadID, dir, idea string, timeout time.Duration) (string, error) {
	prompt := describeOneShotPreamble() + "\n\nThe idea:\n" + idea
	reply, _, err := runInterviewTurn(dir, prompt, "", timeout)
	if err != nil {
		return "", fmt.Errorf("engine turn failed: %w", err)
	}
	item, ok := parseReqItem(reply)
	if !ok || item.Workitem == nil {
		return "", fmt.Errorf("agent did not produce a work item")
	}
	wi := item.Workitem
	kind := strings.ToLower(strings.TrimSpace(wi.Kind))
	if kind != "epic" && kind != "feature" && kind != "task" {
		kind = "task" // default granularity when the agent omits/garbles it
	}
	criteria := make([]api.WorkItemCriterion, 0, len(wi.AcceptanceCriteria))
	for _, c := range wi.AcceptanceCriteria {
		criteria = append(criteria, api.WorkItemCriterion{Text: c.Text, Verify: c.Verify})
	}
	id, err := client.CreateWorkItem(threadID, kind, strings.TrimSpace(wi.Title), "", strings.TrimSpace(wi.Document), item.Readiness, criteria)
	if err != nil {
		return "", fmt.Errorf("record failed: %w", err)
	}
	return fmt.Sprintf("%s %q (readiness %d/5) → %s", kind, strings.TrimSpace(wi.Title), item.Readiness, id), nil
}

// describeOneShotPreamble is the instruction for the headless one-shot path. Unlike interviewPreamble
// it tells the agent it has NO user to ask — produce the best-specified item in one pass — and to CHOOSE
// the granularity (kind) itself, since the web one-shot form doesn't force a kind up front.
func describeOneShotPreamble() string {
	return `You are a requirements analyst turning a rough idea into ONE well-specified backlog item.
You are running HEADLESS — there is NO user to ask, so do NOT ask questions. Produce your single best
specification in one pass. Use the current repository to ground the spec in the real code (read files as
needed). CHOOSE the granularity yourself:
  • epic    — a large effort spanning multiple features (the document is a brief/PRD)
  • feature — one shippable unit
  • task    — a single agent-buildable piece with a concrete definition of done
A to-do list says WHAT; a well-specified item must say HOW — the constraints, edge cases, and a concrete
definition of done. Score readiness 0–5 (5 = an agent could build it with no further clarification). Do
NOT invent scope the user didn't ask for.

Respond with ONE fenced ` + "```json" + ` block, and nothing else, of this exact shape:
{
  "ready": true,
  "readiness": <0-5>,
  "assessment": "<one line: why it's ready, or what's still uncertain>",
  "questions": [],
  "workitem": {
    "kind": "epic|feature|task",
    "title": "<concise imperative title>",
    "document": "<the HOW: constraints, edge cases, approach — the spec body>",
    "acceptance_criteria": [
      {"text": "<verifiable criterion>", "verify": "executable check|adversarial review|behavior review"}
    ]
  }
}`
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
