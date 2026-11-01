package surfacegen

import (
	"bytes"
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/clispec"
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

const overview = `partyline is one command-line tool (` + "`ptln`" + `) with four parts. A session manager
lists and runs every AI coding session on this machine, across engines, directories and git
worktrees, hosted in tmux so a session keeps running when its terminal closes. Repo memory keeps
facts about a repository in that repository, one markdown file per fact under ` + "`memory/`" + `,
committed with the code and written into AGENTS.md, CLAUDE.md and GEMINI.md. A chat room
(` + "`ptln chat`" + `) lets the person and their running sessions exchange messages, and the sessions
answer each other. Loops (experimental) hold a piece of work to a written brief: partyline runs the
repository's checks, has other models review the work, and sends it back until it passes. There is
no server and no account, and partyline sends no telemetry.`

// supportMatrix states the engine story precisely. Overclaiming here is the specific hazard: the
// set of engines that can RUN work is much wider than the set with verified context-thread wiring,
// and conflating them would be a false capability claim in the file models trust most.
const supportMatrix = `Engines: claude, codex, gemini, opencode, goose and antigravity. A
repository's facts reach EVERY one of them through the engine's own memory file (AGENTS.md,
CLAUDE.md, GEMINI.md), which needs no hook and no trust grant. Per-turn refresh needs a hook and is
verified on Claude, Codex and Antigravity; Gemini is wired from its own documented event mapping
but untested. Codex disables an untrusted hook SILENTLY, so partyline reports its trust state as
unknown rather than claiming the hook runs. Models are engine-defined; partyline never holds a
model API key, because inference always runs on your own credentials through your own engine.`

const trustModel = `Trust model: there is nothing to trust remotely, because there is no remote.
Sessions are read from the histories your AI CLIs already write on this machine, and a repository's
facts are files in that repository. Partyline never pushes, merges or opens a pull request, and it
never commits a fact: a fact is written into your working tree and left for you to commit with the
change that taught it. The one commit partyline makes is a loop batch step that passed its checks
and review.`

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
// A model that has only ever read a command list answers "is it merged?" or "did partyline push
// that?" with a guess, and the guess is always the optimistic one. The same holds for facts: an
// agent that has not been told what a fact is FOR writes chatter into the team's durable memory.
// Both failures are cheap to prevent here and expensive everywhere else, so the vocabulary goes in
// the file the reader actually has.
const concepts = `Terms, in the order you meet them:

- **Session** — one AI CLI conversation on this machine, in one directory, under one engine. The
  manager finds them across engines and git worktrees and resumes them, so a session outlives the
  terminal it was started in.

- **Fact** — one small, durable thing the team has established about a repository, stored as a
  markdown file under ` + "`memory/`" + ` IN that repository. Six kinds: overview, decision, constraint,
  contract, gotcha, question. It is NOT a chat log and NOT a transcript. The test a fact has to pass:
  would this still be true and useful to someone who opens this repo in three months and
  was not here? Recording the discussion rather than the outcome is the common misuse.

- **PARTYLINE DOES NOT COMMIT FACTS.** Facts land in your working tree and you commit them with the
  change that taught them. Reporting a fact as shared with the team when it is merely written to
  disk is wrong.

- **Loop** — one piece of work held to a written brief (Goal, Decisions, Constraints, Out of scope,
  Acceptance, Verification, Unknowns). A session starts it with ` + "`ptln loop start`" + `, builds the
  work, and runs ` + "`ptln loop ready`" + `; partyline runs the checks, then other models review the
  diff, and any P1 finding sends the work back, for at most six rounds. ` + "`ptln loop guide`" + ` prints
  the workflow. A **batch** is two or more ordered steps, built one after another on one
  branch by builders partyline starts.

- **LOOPS DECIDE BY DEFAULT.** Unless the person set Decisions to "ask me" in the loop settings
  (ctrl-\ ,), a brief is approved by the loop itself and a disputed finding is ruled on by a model that
  did not build the work. A session must never approve its own brief or rule on its own dispute.

- **PARTYLINE NEVER PUSHES.** It commits a batch step that passed, on the batch's branch, and does
  nothing else to the repository: no push, no merge, no pull request. Builders run with the person's
  own engine setup but cannot push or merge. For a single run the session opens the PR itself.`

// webSurface names the web surface in the vocabulary the site itself uses. Every route named in
// this prose is asserted to exist by checkAnchors below, so a page that gets renamed or deleted
// breaks generation instead of quietly turning this paragraph into a lie.
const controlPlane = `There is no web control plane. partyline is a local CLI: sessions live on
your machine and a repository's facts live in that repository, committed with the code. The site is
documentation only — /docs is the overview and /docs/cli the command reference. Nothing on the web
can execute anything, because nothing on the web is connected to your machine.`

// anchorRoutes are the routes the prose above names by hand. They are checked against the
// extracted surface at generation time: prose is allowed here, unverified prose is not. /describe,
// /dashboard, /threads, /history and /p/[id] were named here until the control plane was removed,
// and this check is what caught the paragraph still describing them.
var anchorRoutes = []string{"/docs", "/docs/cli"}

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
		"Loops (`ptln loop`, experimental) run the repository's checks and then a review by other models (Claude or Codex) every round; a P1 finding sends the work back. Partyline commits only passing batch steps and never pushes, merges or opens a pull request.",
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
		{"https://partyline.sh/docs/memory", "what the team has learned about a repo, kept in the repo"},
		{"https://partyline.sh/docs/loops", "checks and cross-model review of a piece of work, round by round"},
		{"https://partyline.sh/docs/security", "every network and AI request partyline makes, and what it does to your repositories"},
	} {
		fmt.Fprintf(&b, "- [%s](%s): %s\n", d[0], d[0], d[1])
	}
	return b.Bytes(), nil
}

// genLLMsFull renders the complete reference. root is needed to read the docs pages the concepts
// section is extracted from.
// `cs` is the concepts extracted from the audited docs pages; it is named `cs` rather than
// `concepts` because a package-level const of that name already holds the hand-written prose.
func genLLMsFull(root string, s surfacescan.Surface, cs []concept) ([]byte, error) {
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
		// Subcommands were the largest thing this file omitted: a command alone reads as one
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

	b.WriteString("## The website\n\n" + controlPlane + "\n\n")

	fmt.Fprintf(&b, "\n## Scale of the surface\n\n%d commands, %d documentation pages.\n", len(s.OfKind("cli")), len(s.OfKind("web")))
	b.WriteString("\nFull generated reference: `docs/reference/` in the repository.\n")
	fmt.Fprintf(&b, "\nThis document: %s (plain text, regenerated from source on every change).\n", fullURL)
	return b.Bytes(), nil
}

// oneLine flattens a doc comment onto one line for a bullet.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
