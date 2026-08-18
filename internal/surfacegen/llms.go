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
func genLLMsFull(root string, s surfacescan.Surface, reg features.Registry) ([]byte, error) {
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

	b.WriteString("## Install\n\n")
	b.WriteString("- macOS: `brew install partyline-sh/tap/partyline`\n")
	b.WriteString("- Linux: `curl -fsSL https://partyline.sh/install.sh | sh`\n\n")
	b.WriteString("The binary is `ptln` (also installed as `partyline`). `ptln version`, `ptln upgrade`.\n\n")

	b.WriteString("## Engines and models\n\n" + supportMatrix + "\n\n")
	b.WriteString("## Trust model\n\n" + trustModel + "\n\n")

	b.WriteString("## Commands\n\n")
	for _, c := range clispec.Commands {
		if c.Hidden {
			continue
		}
		fmt.Fprintf(&b, "### ptln %s\n\n%s\n\n", c.Name, c.Summary)
		if len(c.Usage) > 0 {
			b.WriteString("```\n" + strings.Join(c.Usage, "\n") + "\n```\n\n")
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
