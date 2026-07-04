// partyline party — the always-on runner that lets an episodic agent (claude)
// take part in a Party (Mode 2: humans + agents on one coordination channel).
//
// Agents are pull, not push: they can't sit and listen. So this process listens
// on the channel (SSE) and WAKES a fresh agent turn only when a message is
// addressed to it (@name / @all / @any, resolved by the backend). The channel log
// is the shared memory the agent re-reads each wake. See docs/PARTY.md.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"partyline.sh/partyline/internal/api"
)

const (
	maxPingPongTurns = 5                      // agent↔agent exchanges before yielding to a human
	wakeDebounce     = 900 * time.Millisecond // coalesce a burst of messages into one wake
	agentTimeout     = 120 * time.Second      // a single agent turn
	historyCap       = 40                     // recent transcript kept for seeding the agent
)

func partyMain(args []string) {
	// Bare `ptln party` → the interactive launcher (pick team + mode, create, bring an agent).
	if len(args) == 0 {
		partyMenu()
		return
	}
	// Subcommands branch off before the single-agent join flow (positional = link).
	if len(args) > 0 && args[0] == "up" {
		partyUp(args[1:])
		return
	}
	var link, name, role, customCmd, model, resumeID string
	var passthru []string   // everything after `--` → handed verbatim to the agent CLI
	var clone bool          // --clone: fork THIS session into a detached headless party agent
	var evidenceFlag bool   // --evidence: force grounded cited-position mode (else by party mode)
	var contextBrief string // --context-file: a session summary to seed the agent (vs raw history)
	engine := "claude"      // which agent CLI to wake; see `engines` registry
	modelSet := false
	maxTurns := -1 // unset sentinel; resolved from the party's mode unless --max-agent-turns given
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			// Native passthrough: e.g. `-- --permission-mode bypassPermissions
			// --allowedTools WebFetch,Bash`. partyline doesn't interpret these — they
			// go straight to claude/gemini/codex, so YOU set the permission posture.
			passthru = append([]string{}, args[i+1:]...)
			break
		}
		switch args[i] {
		case "--engine":
			i++
			if i < len(args) {
				engine = strings.ToLower(args[i])
			}
		case "--name":
			i++
			if i < len(args) {
				name = args[i]
			}
		case "--role":
			i++
			if i < len(args) {
				role = args[i]
			}
		case "--context-file":
			// A briefing (session summary) to seed the agent with — far cheaper + more
			// robust than --resume dragging the whole raw transcript every turn.
			i++
			if i < len(args) {
				if b, e := os.ReadFile(args[i]); e == nil {
					contextBrief = strings.TrimSpace(string(b))
				} else {
					fmt.Fprintf(os.Stderr, "ptln party: --context-file %s: %v\n", args[i], e)
				}
			}
		case "--model":
			i++
			if i < len(args) {
				model = args[i]
				modelSet = true
			}
		case "--resume":
			// Embody an existing local claude session: each turn resumes it (forked,
			// so the original is never modified) so the agent carries that session's
			// real context into the party. Take an id from `ptln llms`.
			i++
			if i < len(args) {
				resumeID = args[i]
			}
		case "--max-agent-turns":
			i++
			if i < len(args) {
				if n, e := strconv.Atoi(args[i]); e == nil && n >= 0 {
					maxTurns = n
				}
			}
		case "--clone":
			// Fork the session you're running this from into a detached headless party
			// agent that carries your full context. See the clone block after parsing.
			clone = true
		case "--evidence":
			evidenceFlag = true
		case "--cmd":
			i++
			if i < len(args) {
				customCmd = args[i] // advanced/testing: run this instead of claude (plain stdout)
			}
		case "-h", "--help":
			partyUsage()
			return
		default:
			if link == "" && !strings.HasPrefix(args[i], "-") {
				link = args[i]
			}
		}
	}
	// --clone: "send a copy of myself into the party." Detect the session you're running
	// from, then spawn a DETACHED headless `ptln party --resume <id>` (which forks that
	// session's full conversation as the agent's context). The clone is a real, persistent
	// party member — in the roster, auto-responding — while this session keeps going.
	if clone {
		if link == "" {
			partyUsage()
			os.Exit(2)
		}
		sid := strings.TrimSpace(os.Getenv("CLAUDE_CODE_SESSION_ID"))
		if sid == "" {
			fmt.Fprintln(os.Stderr, "ptln party --clone: run this from inside a Claude Code session (CLAUDE_CODE_SESSION_ID isn't set).\n"+
				"For another tool, spawn explicitly: ptln party '<link>' --name <you> --resume <id>.")
			os.Exit(2)
		}
		if name == "" {
			name = defaultAgentName()
		}
		// One command, both halves: (1) wire the partyline MCP into THIS session so you can
		// read/post/pull the transcript yourself (reuse `join-mcp`), and (2) spawn the clone.
		mcpOut, mcpErr := exec.Command(selfExe(), "join-mcp", link, "--name", name).CombinedOutput()
		mcpWired := mcpErr == nil
		if !mcpWired {
			fmt.Fprintf(os.Stderr, "(couldn't auto-wire MCP into this session: %v — the clone still spawns; run `ptln join-mcp` manually if you want tools here)\n%s\n", mcpErr, strings.TrimSpace(string(mcpOut)))
		}
		// Spawn the clone with read tools so it can actually cite sources in grounded mode.
		spawnArgs := []string{"party", link, "--name", name, "--resume", sid}
		if role != "" {
			spawnArgs = append(spawnArgs, "--role", role)
		}
		spawnArgs = append(spawnArgs, "--", "--allowedTools", "Read,Grep,Glob")
		logDir := filepath.Join(stateDir(), "clones")
		_ = os.MkdirAll(logDir, 0o700)
		logPath := filepath.Join(logDir, name+".log")
		logf, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "ptln party --clone:", ferr)
			os.Exit(1)
		}
		cmd := exec.Command(selfExe(), spawnArgs...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach: survives this caller
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "ptln party --clone:", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Cloned this session into the party as @%s — a headless agent forked from your full\n"+
			"  context, now live and auto-responding to @%s (log: %s).\n", name, name, logPath)
		if mcpWired {
			fmt.Println("✓ Wired the partyline MCP into THIS session too — run /mcp to connect, then you can\n" +
				"  read_channel / post / read_transcript from here while the clone works the room.")
		}
		return
	}

	if link == "" || name == "" {
		partyUsage()
		os.Exit(2)
	}

	base, id, token, err := parsePartyLink(link)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ptln party:", err)
		os.Exit(2)
	}
	pc := &api.PartyClient{Base: base, ID: id, Token: token}

	// Pull the party's mode template (system preamble + settings). CLI flags override;
	// best-effort, so an older backend / transient error just leaves the defaults.
	var systemPrompt, partyMode string
	if info, e := pc.Info(); e == nil {
		systemPrompt = info.SystemPrompt
		partyMode = info.Mode
		if !modelSet && info.Settings.Model != "" {
			model = info.Settings.Model
		}
		if maxTurns < 0 && info.Settings.MaxAgentTurns != nil {
			maxTurns = *info.Settings.MaxAgentTurns
		}
	}
	if maxTurns < 0 {
		maxTurns = maxPingPongTurns // default brake
	}
	// The party's default model is a claude alias (haiku/…). Only apply it to claude;
	// other engines keep their own default unless the operator set --model explicitly.
	if engine == "claude" && model == "" {
		model = "haiku"
	} else if engine != "claude" && !modelSet {
		model = ""
	}
	if customCmd == "" {
		if _, ok := engines[engine]; !ok {
			fmt.Fprintf(os.Stderr, "ptln party: unknown --engine %q (try: claude, gemini, codex, antigravity — or --cmd \"<command>\")\n", engine)
			os.Exit(2)
		}
	}

	// Announce presence so humans (and /partyline who) can address this agent.
	if _, err := pc.Post(name, fmt.Sprintf("🟢 %s online — role: %s · address `@%s`", name, roleOr(role), name), "system"); err != nil {
		fmt.Fprintln(os.Stderr, "ptln party: can't reach the party:", err)
		os.Exit(1)
	}
	engineLabel := engine
	if model != "" {
		engineLabel += " (" + model + ")"
	}
	if customCmd != "" {
		engineLabel = customCmd
	}
	if len(passthru) > 0 {
		engineLabel += " " + strings.Join(passthru, " ")
	}
	fmt.Printf("ptln party: %q joined — waking %s on @%s / @all / @any (Ctrl-C to leave)\n", name, engineLabel, name)

	ctx, cancel := signalContext()
	defer cancel()

	// Resolve --resume against the local session inventory: accept an id or unique
	// prefix (what `ptln llms` shows), expand to the full UUID, and capture the
	// session's project dir. claude stores sessions per-directory, so we must wake
	// the agent IN that dir or --resume can't find the conversation.
	var resumeDir string
	if resumeID != "" {
		var matched *aiSession
		for _, s := range collectSessions() {
			if s.Tool != "claude" || s.resumeArgv == nil {
				continue
			}
			if s.ID == resumeID || strings.HasPrefix(s.ID, resumeID) {
				sc := s
				matched = &sc
				break
			}
		}
		if matched == nil {
			fmt.Fprintf(os.Stderr, "ptln party: no resumable claude session matches --resume %q (run `ptln llms`)\n", resumeID)
			os.Exit(2)
		}
		resumeID = matched.ID         // full UUID claude needs with --print
		resumeDir = matched.resumeDir // run the agent here so claude finds the session
	}

	// Grounded mode: ON for approach-review parties, or forced with --evidence. In this mode
	// the agent must answer in cited position blocks; we verify each cite and post only what
	// holds up. Other modes are untouched — they post the reply as plain chat, as before.
	evidence := evidenceFlag || partyMode == "approach"
	verifier := ""
	if evidence {
		verifier = "haiku" // independent, cheap check; "" would be citation-gate only
	}
	r := &partyRunner{
		pc: pc, cancel: cancel, name: name, role: role, engine: engine, model: model, customCmd: customCmd,
		passthru: passthru, systemPrompt: systemPrompt, maxAgentTurns: maxTurns, peers: map[string]bool{},
		resumeID: resumeID, resumeDir: resumeDir, evidence: evidence, verifier: verifier, brief: contextBrief,
	}
	r.loop(ctx)
	fmt.Printf("\npl party: %s left.\n", name)
}

type partyRunner struct {
	pc            *api.PartyClient
	cancel        context.CancelFunc // stop the whole runner (e.g. party ended)
	name          string
	role          string
	engine        string          // which agent CLI preset to wake (claude|gemini|codex|…)
	model         string          // model passed to the engine ("" = engine default)
	customCmd     string          // if set, run this instead of an engine (plain stdout, no activity)
	resumeID      string          // claude session id to embody (resumed + forked each turn; "" = fresh)
	resumeDir     string          // the resumed session's project dir (claude scopes sessions by cwd)
	passthru      []string        // native flags handed verbatim to the agent CLI (e.g. --permission-mode)
	systemPrompt  string          // the party mode's preamble, injected into every turn
	maxAgentTurns int             // agent↔agent messages before yielding to a human; 0 = off
	evidence      bool            // grounded mode: require cited+verified positions (approach review)
	verifier      string          // independent model that checks a position's cites ("" = citation-gate only)
	brief         string          // a session summary seeded into the prompt (--context-file)
	peers         map[string]bool // other agent names seen — drives multi-agent prompt rules
	cursor        int64           // highest message id seen (resume point on reconnect)
	history       []api.PartyMsg  // recent transcript, capped
	live          bool            // true after the first `: ready` — before that, msgs are backlog
	pending       []api.PartyMsg  // wake triggers awaiting debounce
	pingpong      int             // consecutive agent messages since the last human message
	yielded       bool            // suppressed-by-pingpong note already posted this streak
}

type streamEvent struct {
	msg   api.PartyMsg
	ready bool
}

func (r *partyRunner) loop(ctx context.Context) {
	events := make(chan streamEvent, 256)
	go r.streamForever(ctx, events)

	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			if ev.ready {
				r.live = true
				continue
			}
			r.observe(ev.msg)
			if r.live && r.shouldWake(ev.msg) {
				r.pending = append(r.pending, ev.msg)
				debounce = time.After(wakeDebounce)
			}
		case <-debounce:
			debounce = nil
			if len(r.pending) > 0 {
				trig := r.pending
				r.pending = nil
				r.wake(ctx, trig)
			}
		}
	}
}

// streamForever keeps the SSE connection up, reconnecting with ?since=cursor so a
// proxy-killed stream is a reconnect, not data loss. Backs off on repeated failure.
func (r *partyRunner) streamForever(ctx context.Context, out chan<- streamEvent) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := r.pc.Stream(ctx, r.name, r.role, r.cursor,
			func() { send(ctx, out, streamEvent{ready: true}) },
			func(m api.PartyMsg) { send(ctx, out, streamEvent{msg: m}) },
		)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, api.ErrPartyClosed) {
			fmt.Println("\npl party: this party has ended.")
			r.cancel() // stop the runner; the agent's job here is done
			return
		}
		if err != nil {
			backoff = min(backoff*2, 15*time.Second)
		} else {
			backoff = time.Second // clean close (our MAX_MS bye) — reconnect promptly
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func send(ctx context.Context, out chan<- streamEvent, ev streamEvent) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}

// observe updates transcript, cursor, and the ping-pong counter for every message
// (whether or not it wakes us).
func (r *partyRunner) observe(m api.PartyMsg) {
	if m.ID > r.cursor {
		r.cursor = m.ID
	}
	r.history = append(r.history, m)
	if len(r.history) > historyCap {
		r.history = r.history[len(r.history)-historyCap:]
	}
	switch {
	case strings.HasPrefix(m.Sender, "user:"):
		r.pingpong = 0 // a human spoke — reset the loop guard
		r.yielded = false
	case strings.HasPrefix(m.Sender, "agent:"):
		r.pingpong++
		if peer := strings.TrimPrefix(m.Sender, "agent:"); peer != r.name {
			r.peers[peer] = true // another agent is here → multi-agent prompt rules apply
		}
	}
}

// shouldWake applies the turn-taking policy: addressed to me, not my own message,
// and not past the agent↔agent ceiling (which yields the floor to a human).
func (r *partyRunner) shouldWake(m api.PartyMsg) bool {
	if m.Sender == "agent:"+r.name {
		return false // self-check: never react to your own post
	}
	if !targetedAtMe(m, r.name) {
		return false
	}
	if r.maxAgentTurns > 0 && strings.HasPrefix(m.Sender, "agent:") && r.pingpong > r.maxAgentTurns {
		if !r.yielded {
			r.yielded = true
			_, _ = r.pc.Post(r.name, "⏸️ pausing — agents have gone back and forth a while. A human should weigh in.", "system")
		}
		return false
	}
	return true
}

// wake spawns a single fresh agent turn seeded from the transcript + the triggers,
// then posts the reply back to the channel as this agent. With the built-in claude
// engine it streams the agent's ACTIVITY (tool calls) to the operator's terminal as
// it happens — so what the agent does is visible and auditable, not secret.
func (r *partyRunner) wake(ctx context.Context, trig []api.PartyMsg) {
	start := time.Now()
	prompt := r.buildPrompt(trig)

	var out string
	var err error
	onActivity := func(activity string) { fmt.Printf("   %s\n", activity) } // live to the operator's terminal
	switch {
	case r.customCmd != "":
		fmt.Printf("\n↯ %s waking (%s)…\n", r.name, r.customCmd)
		out, err = runAgentPlain(ctx, append(splitFields(r.customCmd), r.passthru...), prompt, "", true, false, nil, onActivity)
	default:
		spec := engines[r.engine]
		label := r.engine
		if r.model != "" {
			label += " " + r.model
		}
		fmt.Printf("\n↯ %s waking (%s)…\n", r.name, label)
		argv := append([]string{spec.bin}, spec.args(r.model, r.passthru)...)
		// Embody an existing session: resume it (forked, so the original is never
		// touched) so the agent carries its real context. claude-only for now.
		if r.engine == "claude" && r.resumeID != "" {
			argv = append([]string{spec.bin, "--resume", r.resumeID, "--fork-session"}, spec.args(r.model, r.passthru)...)
			fmt.Printf("   ↳ resuming session %s in %s (forked)\n", r.resumeID, r.resumeDir)
		}
		// Epic B: hand claude the partyline MCP server (read_doc, …) ALONGSIDE the
		// agent's own MCP servers (no --strict — the agent keeps its GitHub/Linear/etc).
		// The token rides the inherited env, never argv. PARTYLINE_NO_MCP opts out.
		var env []string
		if r.mcpActive() {
			argv = append(argv, mcpArgsFor(r.engine)...)
			env = r.mcpEnv()
		}
		out, err = runAgentPlain(ctx, argv, prompt, r.resumeDir, spec.stdin, spec.stream, env, onActivity)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "   ✗ agent turn failed: %v\n", err)
		// Surface the failure IN the room — otherwise the "working…" indicator hangs and
		// humans wait on a silent agent. Post as a normal message so the indicator clears.
		note := evClip(err.Error(), 160)
		if strings.Contains(strings.ToLower(note), "prompt too long") {
			note = "the resumed session is too large for the model's context window — try a smaller session"
		}
		_, _ = r.pc.Post(r.name, "⚠️ couldn't respond this turn — "+note, "msg")
		return
	}
	out = scrubSecrets(strings.TrimSpace(out))
	// Pull any propose-edit blocks out of the reply → pending doc edits (A2.2); what's
	// left is the chat message. Lets one turn both discuss AND propose a doc change.
	out, edits := extractProposeEdits(out)
	for _, e := range edits {
		if _, err := r.pc.ProposeEdit(r.name, e.section, e.body); err != nil {
			fmt.Fprintf(os.Stderr, "   ✗ propose-edit (%s) failed: %v\n", e.section, err)
		} else {
			fmt.Printf("   ✎ proposed edit to %q\n", e.section)
		}
	}
	out = strings.TrimSpace(out)
	if out == "" {
		if len(edits) > 0 {
			fmt.Printf("   ✓ proposed %d edit(s) (%.1fs)\n", len(edits), time.Since(start).Seconds())
		} else {
			fmt.Printf("   · (no reply) %.1fs\n", time.Since(start).Seconds())
		}
		return // nothing left to say in chat
	}
	// Grounded mode: run the evidence gate. If the agent emitted cited position blocks,
	// verify and post the survivors as cited positions and we're done. If it emitted none
	// (a clarifying question, say), fall through and post the reply as ordinary chat.
	if r.evidence {
		if n := r.postPositions(out); n > 0 {
			fmt.Printf("   ✓ posted %d verified position(s) (%.1fs)\n", n, time.Since(start).Seconds())
			return
		}
	}
	if _, err := r.pc.Post(r.name, out, "msg"); err != nil {
		fmt.Fprintf(os.Stderr, "   ✗ post failed: %v\n", err)
		return
	}
	fmt.Printf("   ✓ replied (%.1fs)\n", time.Since(start).Seconds())
}

// postPositions is the evidence gate, run on a grounded agent's reply: parse cited position
// blocks, RE-FETCH each cited source ourselves, verify the claim against it with the
// independent verifier, and post only the survivors as cited "position" messages (the web
// renders them as cards). Returns how many posted; 0 → "no positions, post as normal chat".
func (r *partyRunner) postPositions(out string) int {
	positions, _ := parsePositions(out)
	dir := r.resumeDir
	if dir == "" {
		dir = "."
	}
	posted := 0
	for _, p := range positions {
		if len(p.cites) == 0 {
			continue
		}
		var cites []map[string]any
		verified := false
		for _, c := range p.cites {
			cites = append(cites, map[string]any{"source": c.locator, "locator": c.locator, "quote": c.note})
			if verified {
				continue // a position survives on ONE good cite — skip extra verifier calls
			}
			content := refetch(dir, c.locator)
			if r.verifier == "" {
				verified = strings.TrimSpace(content) != "" // citation-gate only
			} else if ok, _ := verifyClaim(dir, p.claim, c.locator, content, r.verifier); ok {
				verified = true
			}
		}
		if !verified {
			fmt.Printf("   · dropped (no cite held up): %s\n", evClip(p.claim, 80))
			continue
		}
		meta := map[string]any{"kind": "position", "claim": p.claim, "verified": true, "citations": cites}
		if r.verifier != "" {
			meta["verifier"] = r.verifier
		}
		if r.model != "" {
			meta["model"] = r.model
		}
		if _, err := r.pc.PostMeta(r.name, p.claim, "msg", meta); err != nil {
			fmt.Fprintf(os.Stderr, "   ✗ post position failed: %v\n", err)
		} else {
			posted++
			fmt.Printf("   ✓ verified position posted\n")
		}
	}
	return posted
}

func (r *partyRunner) buildPrompt(trig []api.PartyMsg) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, an AI agent in a shared chat. People reach you by writing @%s.\n", r.name, r.name)
	if r.role != "" {
		fmt.Fprintf(&b, "Your role: %s.\n", r.role)
	}
	// The party's mode preamble sets the room's personality/behavior (editable by the
	// starter); fall back to a sane default if the party has none.
	if strings.TrimSpace(r.systemPrompt) != "" {
		b.WriteString("\n" + strings.TrimSpace(r.systemPrompt) + "\n")
	} else {
		b.WriteString("\nBe a helpful, concise teammate. Answer directly and keep it short.\n")
	}
	if r.brief != "" {
		b.WriteString("\nBRIEFING — you represent a working session; this is its summary (your context). " +
			"Answer from it as if it were your own work:\n----- briefing -----\n" + r.brief + "\n----- end briefing -----\n")
	}
	b.WriteString(`
Always: answer directly — don't narrate that you're "an agent in a coordination
channel" or ask who's coordinating. You may use your tools to do work in your own
environment. If something is risky or destructive, or needs a human decision, say so
and stop. Never paste secrets, tokens, or keys. Reply in plain text — it is posted to
the chat verbatim as you.
`)
	if len(r.peers) > 0 {
		names := make([]string, 0, len(r.peers))
		for p := range r.peers {
			names = append(names, "@"+p)
		}
		fmt.Fprintf(&b, "\nOther agents are here (%s). Add only your unique perspective; don't repeat them. "+
			"Hand off by addressing someone: @name (one), @all (everyone), @any (whoever's free).\n", strings.Join(names, ", "))
	}
	// The shared working doc (EPIC A1): the party's living artifact. Inject the current
	// version so the agent edits against reality, and teach the propose-edit convention.
	if doc, _, err := r.pc.GetDoc(); err == nil {
		b.WriteString("\nSHARED WORKING DOC — the party's living artifact (humans approve changes):\n")
		if strings.TrimSpace(doc) == "" {
			b.WriteString("(empty so far)\n")
		} else {
			b.WriteString("----- doc -----\n" + doc + "\n----- end doc -----\n")
		}
		if r.mcpActive() {
			b.WriteString("\nYou have partyline tools (MCP): `read_doc` to re-read the latest, `propose_edit` " +
				"to change a section (a human approves before it merges), and `ask_human` when you need a human " +
				"decision. Use propose_edit for durable decisions, specs, or status — not normal chat. Use the " +
				"tools; do NOT paste propose-edit blocks into your reply.\n")
		} else {
			b.WriteString("\nTo change a section of the doc, include a fenced block in your reply:\n" +
				"```propose-edit section=<Section Name>\n<the full new contents of that section>\n```\n" +
				"A human approves before it merges. Propose edits for durable decisions, specs, or status; " +
				"use normal chat for discussion. You may mix prose and propose-edit blocks in one reply.\n")
		}
	}
	if r.evidence {
		b.WriteString("\nGROUNDED MODE — this is a decision / approach review. RESEARCH with your tools first, " +
			"then answer ONLY inside one or more position blocks, in EXACTLY this format:\n" +
			"```position\nclaim: <one specific, falsifiable sentence>\ncite: <file:line> — <short note>\n```\n" +
			"Every claim needs at least one `cite:` pointing at a real file:line you actually read. If you can't " +
			"cite it, don't say it. No prose outside position blocks. Disagree with other experts where the evidence does.\n")
	}
	b.WriteString("\nRecent chat (oldest first):\n")
	for _, m := range r.history {
		fmt.Fprintf(&b, "[%s] %s\n", m.Sender, oneLine(m.Body))
	}
	b.WriteString("\nYou were just addressed in:\n")
	for _, m := range trig {
		fmt.Fprintf(&b, "[%s] %s\n", m.Sender, m.Body)
	}
	fmt.Fprintf(&b, "\nReply now as %s (or reply with nothing if there's nothing useful to add):\n", r.name)
	return b.String()
}

// ----- helpers -----

// engines maps an --engine preset to how to invoke that agent CLI: bin + args build
// the command; stdin says whether the prompt is delivered on stdin (else appended as
// the final arg); stream marks engines whose JSON output we parse for live activity.
// Adding a new engine = one entry here (the lowest common denominator is plain
// text-in/text-out, which `--cmd` already covers for anything not listed). model is
// passed only when the engine supports it + the operator set it (or it's claude).
type engineSpec struct {
	bin string
	// args builds the CLI flags from the model + the operator's native passthrough
	// (e.g. --permission-mode bypassPermissions). Each engine places passthrough where
	// its parser expects flags — before the prompt-delivering arg.
	args   func(model string, extra []string) []string
	stdin  bool
	stream bool
}

var engines = map[string]engineSpec{
	// Claude Code: stream-json gives us the live tool-call activity view. Prompt on
	// stdin, so passthrough can go at the end.
	"claude": {bin: "claude", stdin: true, stream: true, args: func(m string, extra []string) []string {
		a := []string{"-p", "--output-format", "stream-json", "--verbose"}
		if m != "" {
			a = append(a, "--model", m)
		}
		return append(a, extra...)
	}},
	// OpenAI Codex: `codex exec` reads the prompt from stdin; plain output for now.
	"codex": {bin: "codex", stdin: true, stream: false, args: func(m string, extra []string) []string {
		a := []string{"exec"}
		if m != "" {
			a = append(a, "-m", m)
		}
		return append(a, extra...)
	}},
	// Gemini CLI: `gemini -p <prompt>` — prompt is the final arg, so passthrough must
	// come BEFORE -p.
	"gemini": {bin: "gemini", stdin: false, stream: false, args: func(m string, extra []string) []string {
		a := []string{}
		if m != "" {
			a = append(a, "-m", m)
		}
		a = append(a, extra...)
		return append(a, "-p")
	}},
	// Google Antigravity: `agy -p <prompt>` runs one prompt non-interactively and prints
	// the reply (prompt is the final arg, like gemini). Plain text out — the runner posts
	// the reply. MCP/tool-approval path isn't wired (text-in/out only, as with gemini).
	"antigravity": {bin: "agy", stdin: false, stream: false, args: func(m string, extra []string) []string {
		a := []string{}
		if m != "" {
			a = append(a, "--model", m)
		}
		a = append(a, extra...)
		return append(a, "-p")
	}},
}

func splitFields(s string) []string { return strings.Fields(s) }

// mcpActive reports whether this turn hands the agent the partyline MCP tools. claude is
// on by default (verified end-to-end). codex is wired but UNVERIFIED — gated behind
// PARTYLINE_MCP_EXPERIMENTAL so it can't regress codex party turns until tested live.
// Not for --cmd agents, and PARTYLINE_NO_MCP is a global kill switch.
func (r *partyRunner) mcpActive() bool {
	if r.customCmd != "" || os.Getenv("PARTYLINE_NO_MCP") != "" {
		return false
	}
	switch r.engine {
	case "claude":
		return true
	case "codex":
		return os.Getenv("PARTYLINE_MCP_EXPERIMENTAL") != ""
	}
	return false // gemini: no per-invocation MCP config + auth-coupled ~/.gemini — see B.3
}

// mcpEnv is the env the party-mcp server reads (party token + identity). The runner
// sets it on the engine process; the engine's spawned stdio MCP server inherits it —
// so the token reaches party-mcp without ever appearing in a command line.
func (r *partyRunner) mcpEnv() []string {
	return []string{
		"PARTYLINE_PARTY_BASE=" + r.pc.Base,
		"PARTYLINE_PARTY_ID=" + r.pc.ID,
		"PARTYLINE_PARTY_TOKEN=" + r.pc.Token,
		"PARTYLINE_AGENT_NAME=" + r.name,
	}
}

// selfExe is the path to this running binary — the command the engine spawns as the
// `party-mcp` stdio MCP server.
func selfExe() string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return os.Args[0]
}

// mcpArgsFor returns the engine flags that register the partyline MCP server. The config
// carries only the command (this binary, `ptln party-mcp`) — no secrets — so it's safe in
// argv; the party token arrives via the inherited env (mcpEnv), never a command line. We
// add our server ALONGSIDE the agent's own MCP servers (no claude --strict-mcp-config) —
// keeping the agent's GitHub/Linear/etc is the whole point.
func mcpArgsFor(engine string) []string {
	exe := selfExe()
	switch engine {
	case "claude":
		cfg := fmt.Sprintf(`{"mcpServers":{"partyline":{"command":%q,"args":["party-mcp"]}}}`, exe)
		// Pre-authorize our OWN tools so a headless agent can use them without the operator
		// opening --permission-mode (it can't answer a prompt). Safe by design: read_doc is
		// read-only and every write tool is human-approved server-side. The agent's other
		// tools keep the operator's chosen posture.
		return []string{"--mcp-config", cfg, "--allowedTools", strings.Join(mcpToolNames(), ",")}
	case "codex":
		// codex exec: register a stdio MCP server via inline TOML config overrides. Token
		// rides the inherited env (cmd.Env), same as claude. UNVERIFIED — codex's headless
		// MCP-tool approval behavior still needs a live check.
		return []string{
			"-c", fmt.Sprintf("mcp_servers.partyline.command=%q", exe),
			"-c", `mcp_servers.partyline.args=["party-mcp"]`,
		}
	}
	return nil
}

// mcpToolNames is the claude-namespaced names of the partyline MCP tools (mcp__<server>__<tool>).
func mcpToolNames() []string {
	names := make([]string, 0, len(toolDefs))
	for _, t := range toolDefs {
		names = append(names, "mcp__partyline__"+t["name"].(string))
	}
	return names
}

type proposedEdit struct {
	section string
	body    string
}

// extractProposeEdits pulls ```propose-edit section=<name> … ``` fenced blocks out of an
// agent reply, returning the reply with those blocks removed plus the parsed edits. A
// block with no resolvable section is dropped (not posted raw). Engine-agnostic — this is
// the universal text-floor convention (A2.2), so it works for every CLI incl. --cmd.
func extractProposeEdits(text string) (string, []proposedEdit) {
	lines := strings.Split(text, "\n")
	var kept []string
	var edits []proposedEdit
	for i := 0; i < len(lines); {
		info := strings.TrimSpace(lines[i])
		if strings.HasPrefix(info, "```") && strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(info, "```")), "propose-edit") {
			section := parseSectionAttr(strings.TrimSpace(strings.TrimPrefix(info, "```")))
			var bodyLines []string
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) != "```" {
				bodyLines = append(bodyLines, lines[j])
				j++
			}
			if section != "" {
				edits = append(edits, proposedEdit{section: section, body: strings.Trim(strings.Join(bodyLines, "\n"), "\n")})
			}
			i = j + 1 // skip the block + its closing fence (or run to EOF)
			continue
		}
		kept = append(kept, lines[i])
		i++
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), edits
}

// parseSectionAttr reads section=<name> from a fence info string, supporting an optional
// quoted value for multi-word sections (section="Open Questions").
func parseSectionAttr(info string) string {
	idx := strings.Index(info, "section=")
	if idx < 0 {
		return ""
	}
	s := strings.TrimSpace(info[idx+len("section="):])
	if strings.HasPrefix(s, "\"") {
		if end := strings.Index(s[1:], "\""); end >= 0 {
			return s[1 : 1+end]
		}
		return strings.Trim(s, "\"")
	}
	if sp := strings.IndexAny(s, " \t"); sp >= 0 {
		return s[:sp]
	}
	return s
}

// runAgentPlain runs an agent turn. argv is the command; the prompt is delivered on
// stdin (stdin=true) or appended as the final arg. With stream=true the engine's
// stream-json output (claude) is parsed for the final answer + live tool activity via
// onActivity; otherwise stdout is captured verbatim (works for any text-out CLI).
func runAgentPlain(ctx context.Context, argv []string, prompt, dir string, stdin, stream bool, env []string, onActivity func(string)) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("no command")
	}
	cctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	full := argv
	if !stdin {
		full = append(append([]string{}, argv...), prompt)
	}
	cmd := exec.CommandContext(cctx, full[0], full[1:]...)
	if dir != "" {
		cmd.Dir = dir // claude scopes sessions by cwd — resume must run in the project dir
	}
	if len(env) > 0 {
		// Extra vars for the engine — and, by inheritance, the MCP server it spawns
		// (that's how the party token reaches party-mcp without ever hitting argv).
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	fail := func(werr error) error {
		if cctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("agent timed out after %s", agentTimeout)
		}
		if m := strings.TrimSpace(stderr.String()); m != "" {
			return fmt.Errorf("%v: %s", werr, m)
		}
		return werr
	}

	if !stream {
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			return "", fail(err)
		}
		return stdout.String(), nil
	}

	// stream-json (claude): parse events as they arrive so we can surface tool calls.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	rd := bufio.NewReader(pipe)
	var finalText, lastText string
	for {
		line, rerr := rd.ReadString('\n') // events can be very large (no Scanner cap)
		if len(line) > 0 {
			var ev claudeEvent
			if json.Unmarshal([]byte(line), &ev) == nil {
				switch ev.Type {
				case "assistant":
					for _, b := range ev.Message.Content {
						switch b.Type {
						case "tool_use":
							if onActivity != nil {
								onActivity("→ " + b.Name + briefInput(b.Input))
							}
						case "text":
							if s := strings.TrimSpace(b.Text); s != "" {
								lastText = b.Text
							}
						}
					}
				case "result":
					if ev.Result != "" {
						finalText = ev.Result
					}
				}
			}
		}
		if rerr != nil {
			break // EOF — the engine finished streaming
		}
	}
	werr := cmd.Wait()
	if finalText == "" {
		finalText = lastText // no result event (e.g. interrupted) — fall back to last text
	}
	if finalText == "" && werr != nil {
		return "", fail(werr)
	}
	return finalText, nil
}

// claudeEvent is the subset of claude's --output-format stream-json events we read.
type claudeEvent struct {
	Type    string `json:"type"`    // system|assistant|user|result|...
	Subtype string `json:"subtype"` // init|success|...
	Result  string `json:"result"`  // final answer (type=result)
	Message struct {
		Content []struct {
			Type  string          `json:"type"` // text|thinking|tool_use|tool_result
			Text  string          `json:"text"`
			Name  string          `json:"name"`  // tool_use
			Input json.RawMessage `json:"input"` // tool_use
		} `json:"content"`
	} `json:"message"`
}

// briefInput renders a one-line summary of a tool call's input for the activity log:
// the command/path/etc. when present, else a short slice of the raw JSON.
func briefInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
			if v, ok := m[k].(string); ok && v != "" {
				return ": " + oneLine(truncate(v, 120))
			}
		}
	}
	return ": " + oneLine(truncate(string(raw), 80))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// parsePartyLink pulls the origin, party id, and token out of a join link of the
// form https://host/p/<id>#t=<token>. The token lives in the #fragment (never sent
// to a server on a normal page load) — we read it locally here.
func parsePartyLink(link string) (base, id, token string, err error) {
	link = strings.TrimSpace(strings.Trim(link, `'"`))
	u, e := url.Parse(link)
	if e != nil {
		return "", "", "", e
	}
	base = u.Scheme + "://" + u.Host
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "p" {
		id = parts[1]
	}
	for _, kv := range strings.Split(u.Fragment, "&") {
		if strings.HasPrefix(kv, "t=") {
			token = strings.TrimPrefix(kv, "t=")
		}
	}
	if base == "://" || id == "" || token == "" {
		return "", "", "", fmt.Errorf("not a valid party link (expected .../p/<id>#t=<token>)")
	}
	return base, id, token, nil
}

func targetedAtMe(m api.PartyMsg, name string) bool {
	to, ok := m.Meta["to"].([]any)
	if !ok {
		return false
	}
	for _, t := range to {
		if s, ok := t.(string); ok && strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// scrubSecrets is a naive last-line-of-defense redaction on what an agent posts —
// the team-dynamics prompt is the primary control, this catches obvious slips.
var secretRe = regexp.MustCompile(
	`(?i)(sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|xox[b-p]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)

func scrubSecrets(s string) string { return secretRe.ReplaceAllString(s, "[redacted]") }

func roleOr(role string) string {
	if role == "" {
		return "(unspecified)"
	}
	return role
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}

func partyUsage() {
	fmt.Println(`Usage: ptln party                                       interactive: start or join a party (no flags)
       ptln party '<join-link>' --name <name> [--role "<what it does>"] [--model <m>]
       ptln party up [partyline.yml]                  bring up a whole room (see: ptln party up --help)

Joins a Party (a humans + agents chat) as an agent. The runner watches the chat and
wakes your agent for one turn whenever it's addressed with @name, @all, or @any. As
it runs, the agent's activity (tool calls) streams to this terminal so you can see
and audit what it's doing. Get the join link from /partyline party in Slack or a
party's page on the web.

  --name   how you're addressed in the chat (e.g. db-agent)
  --role   one line on what this agent does (shown to others + the agent itself)
  --engine which agent CLI to wake: claude (default) · gemini · codex · antigravity.
           claude streams its tool activity to this terminal; others post their reply (plain).
  --model  model for the engine (overrides the party default). For claude: haiku/
           sonnet/opus. For gemini/codex: that engine's model id (else its default).
  --resume <id>  embody an existing claude session (id from ` + "`ptln llms`" + `): each turn
           resumes it FORKED, so the agent carries that session's real context into
           the party while the original session is never modified. (claude only)
  --clone  run from INSIDE a Claude Code session — the one-command "send myself in": it
           (1) wires the partyline MCP into THIS session (so you can read/post/read_transcript
           here — run /mcp to connect), and (2) spawns a detached headless agent forked from
           this session's full context that joins and auto-responds. Your session keeps going;
           the clone logs to ~/.partyline/clones/<name>.log until the party ends or you kill it.
  --max-agent-turns  agent↔agent messages before yielding to a human (overrides the
           mode default; 0 = no brake, e.g. for a debate)
  --cmd    escape hatch: run ANY command instead of an engine (prompt on stdin,
           stdout posted back; no activity view). e.g. --cmd "ollama run llama3"
  --       everything after -- is passed VERBATIM to the agent CLI, so you set the
           permission posture yourself with that tool's NATIVE flags. e.g.:
             ptln party '<link>' --name dev -- --permission-mode bypassPermissions
             ptln party '<link>' --name dev -- --allowedTools "WebFetch,Bash,Edit"
           (headless agents can't answer permission prompts, so pre-authorize here —
           otherwise the agent gets stuck asking for access it can't be granted.)

The party's mode (Bot chat, Incident war room, …) sets the default instructions,
model, and brake; flags here override them.

Each party has a shared working doc (a PRD, incident timeline, …). The agent is given
it every turn and can propose changes by emitting a ` + "```propose-edit section=<name>```" + `
block; the runner files it as a pending edit for a human to approve on the web — it
never edits the doc directly.`)
}
