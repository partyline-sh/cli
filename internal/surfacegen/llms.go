package surfacegen

import (
	"bytes"
	"fmt"
	"partyline.sh/partyline/internal/features"
	"strings"

	"partyline.sh/partyline/internal/clispec"
	"partyline.sh/partyline/internal/surface"
	"partyline.sh/partyline/internal/surfacescan"
)

// llms.txt and llms-full.txt: the files a language model reads to learn what partyline is.
//
// These were hand-written and last touched on 5 July. They describe a three-feature product —
// session manager, shared shell, Slack parties — with no fleet, no crank, no work board, no verify
// gates, no context threads, no provisioning. Every model that has read them since has been given a
// picture of partyline that is about a year out of date.
//
// Hand-writing a machine-consumption artifact is indefensible, so the LISTS below are generated.
// The PROSE is still written by a person, but it lives here, in one place, next to the lists it
// describes — rather than in a text file nobody opens. That is the honest split: a generator cannot
// write positioning, but it can guarantee that the capability list next to the positioning is true.
//
// RULE FOR EDITING THE PROSE: it must survive `ptln --help`. If a sentence here claims something a
// reader cannot then do, it is a bug of the same kind as a stale command list.

const overview = `partyline is one command-line tool (` + "`ptln`" + `) plus an optional web control plane for
running software work across humans and AI agents. The agents run on machines you own — your laptop,
your server — never on partyline's infrastructure. It does four things: manages and multiplexes your
local AI CLI sessions; shares a live terminal with people and agents over an end-to-end-encrypted
channel; runs autonomous work through a fleet of your own machines, each task in an isolated git
worktree, gated before it merges; and keeps durable shared context that survives across people,
machines, and engines.`

// supportMatrix states the engine story precisely. Overclaiming here is the specific hazard: the
// set of engines that can RUN work is much wider than the set with verified context-thread wiring,
// and conflating them would be a false capability claim in the file models trust most.
const supportMatrix = `Engines: work runs on Claude Code, Codex, Gemini CLI, opencode, or goose;
antigravity is available for interactive and planning use only, because its sole headless mode is a
permissions bypass partyline will not pass. Context Threads (the shared-memory MCP tools) are
verified on Claude Code only today — Codex is wired but experimental and gated behind an env flag,
and Gemini has no MCP wiring at all. Models are engine-defined and free-form; partyline never holds
a model API key, because inference always runs on your own credentials through your own engine.`

const trustModel = `Trust model: the control plane holds DATA and never commands. A run carries a
project LABEL, and the machine's own local registry resolves that label to an absolute path — no
value from the server ever becomes a path or an argv. Shared sessions are end-to-end encrypted
(Noise); the relay forwards ciphertext it cannot read, and the key travels in the join link's URL
fragment, which browsers never send to a server. Joiners are view-only until the host grants the
keyboard.`

// fullURL is the address this document is published at, and it is a CONTRACT, not a detail.
//
// The reader this file exists for has installed nothing: no binary, no MCP server, no account. The
// only thing they can be handed is a URL, and that URL ends up pasted into prompts and bookmarked by
// people who will never see a redirect we add later. So it is fixed here, stated inside both
// generated files, and changing it is a breaking change — serve the old path forever if it ever
// moves. `web/public/` is served verbatim by the Next app, so the bytes at this URL ARE the bytes
// `make surface-gen` produced; there is no hand-maintained copy to drift.
const fullURL = "https://partyline.sh/llms-full.txt"

// concepts is the part of the document a command list cannot supply.
//
// A model that has only ever read a command list answers "what happened when I filed that plan?"
// with a guess, and the guess is always the optimistic one — that work started. It did not. The
// same holds for context threads: an agent that has not been told what a thread is FOR treats it
// as a chat log and writes chatter into the team's durable memory. Both failures are cheap to
// prevent here and expensive everywhere else, so the vocabulary goes in the file the reader
// actually has.
const concepts = `Terms, in the order you meet them:

- **Project** — one repository, identified by its ` + "`origin`" + ` remote rather than by a local path (the
  same repo is at a different path on everyone else's machine). A project owns a base branch, the
  machines that may build for it, and a context thread.

- **Context thread** — the team's durable shared memory for a project: the decisions, constraints
  and interface contracts that outlive any one session, attributed to the person or agent that
  recorded them. An agent reads it with ` + "`recall`" + ` and adds to it with ` + "`remember`" + `; on the CLI that is
  ` + "`ptln thread recall <id>`" + ` and ` + "`ptln thread remember <id> <kind> \"<fact>\"`" + ` (kinds: decision,
  constraint, contract, question, note). It is NOT a chat log and NOT a transcript — it holds small,
  high-signal facts, so that a fresh session tomorrow, on another machine, on another engine, starts
  from what the team already settled instead of re-litigating it. Recording conversation in it is the
  common misuse; record the outcome, not the discussion.

- **Work item** — one planned, bounded piece of work on the board. The Planning agent (` + "`ptln plan`" + `,
  or the planning_open → planning_note → planning_finalize tools over MCP) interviews an idea into
  items rather than accepting hand-assembled ones. Finalize applies the same specificity gate the
  board applies at start time: it REFUSES work that does not name a target, does not carry acceptance
  criteria including at least one EXECUTABLE check, or still has an unanswered open question. Those
  refusals are the feature — an unanswered question belongs back with the human, not decided quietly
  by the agent.

- **FILING IS NOT STARTING.** Finalizing a plan FILES it; nothing runs. A filed item starts only when
  it is promoted, which dispatches a worker onto a machine in your fleet. An agent that has just filed
  work must say so plainly rather than implying a build is under way — reporting queued work as
  started is the single most common wrong answer about this product.

- **Run** — one machine building one work item. It happens in an isolated git worktree off the
  project's base branch, leaves a reviewable branch, and — depending on the project's merge policy
  (manual, pr, or auto) — opens a pull request. Runs are executed by ` + "`ptln work`" + ` (one task),
  ` + "`ptln crank`" + ` (a worklist), or the always-on ` + "`ptln daemon`" + ` (work the control plane asks this
  machine to do). Nothing runs on partyline's infrastructure.

- **Trust gates** — what stands between a finished run and your main branch: the repo's own
  acceptance checks, plus an independent adversarial reviewer on a different model. A task that fails
  is quarantined for a human. It is never merged on the strength of the agent's own say-so.

- **Session** — a live terminal. ` + "`ptln`" + ` on its own browses and multiplexes the AI CLI sessions
  already on this machine (local-only, no account needed); ` + "`ptln start`" + ` shares your shell over an
  end-to-end-encrypted channel and prints a join link. Joiners are view-only until the host grants
  the keyboard.

- **Party** — a coordination channel where people and agents work together, from the web, Slack, or
  the CLI. Each agent still does its real work in its own environment; the party carries directives,
  status and hand-offs. Unlike a session it is encrypted in transit and at rest but NOT end to end,
  so it is for coordination, not secrets.`

// controlPlane names the web surface in the vocabulary the app itself uses. Every route named in
// this prose is asserted to exist by checkAnchors below, so a page that gets renamed or deleted
// breaks generation instead of quietly turning this paragraph into a lie.
const controlPlane = `The optional web control plane is a work board. You file work at /work (or from
the CLI), it queues, and the daemon on one of your own machines picks it up; /fleet shows those
machines and what each is running, /runs and /runs/<id> show a run's live log and its gate verdicts,
/threads holds the shared context, and /projects is the label that joins machines, runs and threads
(the label resolves to a real path only on the machine itself).
Nothing on the board executes anything by itself — a machine has to be running ` + "`ptln daemon`" + ` and to
already know the project label locally.`

// anchorRoutes are the routes the prose above names by hand. They are checked against the extracted
// surface at generation time: prose is allowed here, unverified prose is not.
var anchorRoutes = []string{"/work", "/fleet", "/runs", "/runs/[id]", "/threads", "/projects"}

func checkAnchors(s surfacescan.Surface) error {
	have := map[string]bool{}
	for _, it := range s.OfKind("web") {
		have[it.Name] = true
	}
	for _, r := range anchorRoutes {
		if !have[r] {
			return fmt.Errorf("llms prose names the page %s, which no longer exists in the App Router — "+
				"fix the sentence in internal/surfacegen/llms.go rather than the route list", r)
		}
	}
	return nil
}

// appPages are the control-plane routes, extracted from the App Router: everything that is not a
// marketing page and not a Next.js grouping/intercept/catch-all artifact. A model that knows these
// can navigate the product; a hand-kept copy of this list would be wrong within a week.
func appPages(s surfacescan.Surface) []string {
	var out []string
	for _, it := range s.OfKind("web") {
		if strings.Contains(it.Source, "(marketing)") || strings.Contains(it.Name, "(") || strings.Contains(it.Name, "[...") {
			continue
		}
		out = append(out, it.Name)
	}
	return out
}

func genLLMsShort(s surfacescan.Surface) ([]byte, error) {
	if err := checkAnchors(s); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("# partyline\n\n> ")
	b.WriteString(oneLine(overview))
	b.WriteString(" Install on macOS with `brew install partyline-sh/tap/partyline`, or on Linux with `curl -fsSL https://partyline.sh/install.sh | sh`.\n\n")

	b.WriteString("Key facts:\n")
	for _, f := range []string{
		"One binary, `ptln`. macOS and Linux. MIT licensed.",
		oneLine(supportMatrix),
		oneLine(trustModel),
		"Autonomous work runs one task per isolated git worktree and leaves a reviewable branch. A verify gate — the repo's own acceptance checks, an independent adversarial reviewer on a different model, and optionally a vision reviewer that renders the change — runs before anything merges. A task that fails is quarantined for a human, never merged.",
		"The session manager is local-only and needs no account: it reads the histories your AI CLIs already write on this machine.",
		oneLine(controlPlane),
	} {
		b.WriteString("- " + f + "\n")
	}

	b.WriteString("\n## Commands\n")
	for _, c := range clispec.Commands {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(&b, "- `ptln %s`: %s\n", c.Name, c.Summary)
	}

	b.WriteString("\n## Docs\n")
	// The full document comes FIRST and is described as fetchable, because the reader most likely to
	// need it — an agent helping someone evaluate or install partyline — has no MCP connection and no
	// binary, and this line is the only thing that tells it a complete reference exists at all.
	fmt.Fprintf(&b, "- [%s](%s): the COMPLETE reference, one plain-text file. No account, no install — fetch it directly.\n", fullURL, fullURL)
	for _, d := range [][2]string{
		{"https://partyline.sh/docs", "overview of every feature"},
		{"https://partyline.sh/docs/cli", "command reference"},
		{"https://partyline.sh/docs/trust-gates", "how autonomous work is verified before it merges"},
		{"https://partyline.sh/docs/context-threads", "shared memory across people, machines, and engines"},
		{"https://partyline.sh/docs/security", "the encryption and trust model"},
	} {
		fmt.Fprintf(&b, "- [%s](%s): %s\n", d[0], d[0], d[1])
	}
	return b.Bytes(), nil
}

// genLLMsFull takes root and reg so it can append the self-host configuration section — the one
// audience that needs it (an agent helping someone install partyline) has no repo to read it from.
// `cs` is the concepts extracted from the audited docs pages; it is named `cs` rather than
// `concepts` because a package-level const of that name already holds the hand-written prose.
func genLLMsFull(root string, s surfacescan.Surface, reg features.Registry, cs []concept) ([]byte, error) {
	if err := checkAnchors(s); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("# partyline — full reference for language models\n\n")
	b.WriteString(overview)
	b.WriteString("\n\n")
	b.WriteString("This file is GENERATED from the source tree. The command list, the vocabularies, and the\n")
	b.WriteString("endpoint count below are extracted from the code, not written by hand, so they describe the\n")
	b.WriteString("version that produced them rather than the version someone last remembered to document.\n\n")
	fmt.Fprintf(&b, "It is published as plain text at %s — no account, no install, nothing to\n", fullURL)
	b.WriteString("connect. Fetch it, or paste it into an assistant, and you have the whole product in one file.\n")
	b.WriteString("That address is stable: treat any change to it as a breaking change.\n\n")

	b.WriteString("## Install\n\n")
	b.WriteString("- macOS: `brew install partyline-sh/tap/partyline`\n")
	b.WriteString("- Linux: `curl -fsSL https://partyline.sh/install.sh | sh`\n\n")
	b.WriteString("The binary is `ptln` (also installed as `partyline`). `ptln version`, `ptln upgrade`.\n\n")

	b.WriteString("## Engines and models\n\n" + supportMatrix + "\n\n")
	b.WriteString("## Trust model\n\n" + trustModel + "\n\n")
	b.WriteString("## How the work model fits together\n\n" + concepts + "\n\n")

	// Concepts before commands: an agent that reads the command list first and stops there is
	// exactly the reader this section exists for.
	b.WriteString(genConcepts(cs))

	b.WriteString("## Commands\n\n")
	for _, c := range clispec.Commands {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(&b, "### ptln %s\n\n%s\n\n", c.Name, c.Summary)
		if len(c.Usage) > 0 {
			b.WriteString("```\n" + strings.Join(c.Usage, "\n") + "\n```\n\n")
		}
		// Subcommands were the largest thing this file omitted: `ptln daemon` alone reads as one
		// capability when it is six, and a reader with only the top-level list invents the rest.
		for _, sub := range c.Subs {
			name, doc, found := strings.Cut(sub, ": ")
			if !found {
				name, doc = sub, ""
			}
			fmt.Fprintf(&b, "- `ptln %s %s`", c.Name, name)
			if doc != "" {
				fmt.Fprintf(&b, " — %s", doc)
			}
			b.WriteString("\n")
		}
		if len(c.Subs) > 0 {
			b.WriteString("\n")
		}
		for _, f := range c.Flags {
			left := "--" + f.Name
			if f.Arg != "" {
				left += " " + f.Arg
			}
			fmt.Fprintf(&b, "- `%s` — %s\n", left, f.Doc)
		}
		if len(c.Flags) > 0 {
			b.WriteString("\n")
		}
	}

	b.WriteString("## The web control plane\n\n" + controlPlane + "\n\n")
	b.WriteString("Pages, extracted from the App Router (marketing and docs pages omitted):\n\n")
	for _, p := range appPages(s) {
		fmt.Fprintf(&b, "- `%s`\n", p)
	}
	b.WriteString("\n")

	b.WriteString("## Values the API accepts\n\n")
	for _, v := range surface.All() {
		fmt.Fprintf(&b, "- **%s** — %s\n  Values: `%s`\n", v.Name, oneLine(v.Doc), strings.Join(v.Keys(), "`, `"))
	}

	fmt.Fprintf(&b, "\n## Scale of the surface\n\n%d HTTP endpoints, %d web pages, %d database tables, %d commands.\n",
		len(s.OfKind("api")), len(s.OfKind("web")), len(s.OfKind("table")), len(s.OfKind("cli")))
	b.WriteString("\nFull generated reference: `docs/reference/` in the repository.\n")
	fmt.Fprintf(&b, "\nThis document: %s (plain text, regenerated from source on every change).\n", fullURL)
	// The configuration surface of a self-hosted box, generated from the same registry the env
	// example is. Appended here rather than kept as its own file so a reader who fetches one URL has
	// everything — see the self-host docs constraint: they have not installed anything yet.
	selfHost, err := genSelfHost(root, reg)
	if err != nil {
		return nil, err
	}
	b.Write(selfHost)
	return b.Bytes(), nil
}
