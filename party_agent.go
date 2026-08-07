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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"partyline.sh/partyline/internal/api"
	eng "partyline.sh/partyline/internal/engine"
)

const (
	maxPingPongTurns = 5                      // agent↔agent exchanges before yielding to a human
	wakeDebounce     = 900 * time.Millisecond // coalesce a burst of messages into one wake
	agentIdleTimeout = 5 * time.Minute        // kill a turn only after this long with NO output (stuck, not slow)
	agentMaxTimeout  = 20 * time.Minute       // absolute backstop so a runaway turn can't run forever
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
	// The human half of switch_context (C2) — see party_context_cmd.go.
	if args[0] == "context" {
		partyContext(args[1:])
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
		// Read tools so the clone can cite sources in grounded mode — PLUS our own MCP tools.
		//
		// Both matter, and it has to be ONE flag. mcpArgsFor() also emits --allowedTools (to
		// pre-authorize read_doc/propose_edit, since a headless agent cannot answer a permission
		// prompt), and claude takes the LAST --allowedTools it is given. Passing a second one here
		// silently clobbered the MCP list, so a cloned agent's every propose_edit/read_doc call was
		// blocked — and the agent, having no way to see why, reported it as "waiting for the
		// operator to grant permission". Nobody could grant it: no such toggle exists.
		spawnArgs = append(spawnArgs, "--", "--allowedTools",
			strings.Join(append([]string{"Read", "Grep", "Glob"}, mcpToolNames()...), ","))
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
	var systemPrompt, partyMode, serverModel string
	var groundedOverride *bool // server-sent grounded flag (nil → mode-based default)
	var idleSec, maxSec *int   // server-sent turn timeouts (nil → daemon defaults)
	if info, e := pc.Info(); e == nil {
		systemPrompt = info.SystemPrompt
		partyMode = info.Mode
		serverModel = info.Settings.Model
		groundedOverride = info.Settings.Grounded
		idleSec, maxSec = info.Settings.IdleTimeoutSec, info.Settings.MaxTimeoutSec
		if maxTurns < 0 && info.Settings.MaxAgentTurns != nil {
			maxTurns = *info.Settings.MaxAgentTurns
		}
	}
	if maxTurns < 0 {
		maxTurns = maxPingPongTurns // default brake
	}
	model = resolvePartyModel(engine, model, modelSet, serverModel)
	if customCmd == "" {
		if !eng.Valid(engine) {
			fmt.Fprintf(os.Stderr, "ptln party: unknown --engine %q (try: claude, gemini, codex, antigravity — or --cmd \"<command>\")\n", engine)
			os.Exit(2)
		}
	}

	// Announce presence so humans (and /partyline who) can address this agent.
	//
	// KEEP THE ID. It is this process's watermark: every message numbered above it was posted after
	// we joined, so it is a real prompt no matter whether it reaches us live or in the stream's
	// initial replay. Discarding it is what made a message sent in the first moments after an agent
	// appeared vanish silently — see joinID below.
	joinID, err := pc.Post(name, fmt.Sprintf("🟢 %s online — role: %s · address `@%s`", name, roleOr(role), name), "system")
	if err != nil {
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

	// Grounded (cited-position / evidence) mode. Server-authoritative: the control plane sends
	// `grounded` per party (so flipping it is a web deploy, not a daemon release). The daemon only
	// falls back to the mode-based default for an older backend that sends nothing — and NEVER
	// grounds a describe party, whichever way the flag lands (grounding kills its facilitation loop).
	evidence := grounded(groundedOverride, evidenceFlag, partyMode)
	verifier := ""
	if evidence {
		verifier = "haiku" // independent, cheap check; "" would be citation-gate only
	}
	// Turn timeouts, also server-driven (bounded): kill a turn after idle silence, with an absolute
	// backstop. Clamped to sane ranges so a bad/hostile settings value can't disable the guard.
	idleTimeout := clampTimeout(idleSec, agentIdleTimeout, 30*time.Second, 15*time.Minute)
	maxTimeout := clampTimeout(maxSec, agentMaxTimeout, 2*time.Minute, 60*time.Minute)
	// Tell the room what's answering. The web footer names the host and CLI version already; engine
	// and model are the two facts only THIS process knows, because resolvePartyModel may have fallen
	// through to the engine's own default, which the server cannot see. Carried on the presence
	// heartbeat rather than posted separately — presence is already the "who is here" channel.
	// `engine` is already the resolved engine name; empty means the claude default, spelled out here
	// rather than left blank so the footer never has to say "unknown" for the commonest case.
	if engine != "" {
		pc.Engine = engine
	} else {
		pc.Engine = "claude"
	}
	pc.Model = model
	r := &partyRunner{
		pc: pc, cancel: cancel, name: name, role: role, engine: engine, model: model, customCmd: customCmd,
		passthru: passthru, systemPrompt: systemPrompt, maxAgentTurns: maxTurns, peers: map[string]bool{},
		resumeID: resumeID, resumeDir: resumeDir, evidence: evidence, verifier: verifier, brief: contextBrief,
		idleTimeout: idleTimeout, maxTimeout: maxTimeout, joinID: joinID,
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
	idleTimeout   time.Duration   // kill a turn after this long with no output (server-driven, clamped)
	maxTimeout    time.Duration   // absolute per-turn backstop (server-driven, clamped)
	brief         string          // a session summary seeded into the prompt (--context-file)
	threadCtx     string          // the party's linked-thread canon, injected as background (fetched once)
	threadCtxSet  bool            // whether threadCtx has been resolved (so a fetch failure isn't retried every turn)
	peers         map[string]bool // other agent names seen — drives multi-agent prompt rules
	cursor        int64           // highest message id seen (resume point on reconnect)
	history       []api.PartyMsg  // recent transcript, capped
	live          bool            // true after the first `: ready`
	// joinID is the id of THIS process's own "online" post. Anything numbered above it was written
	// after we joined and is a genuine prompt; anything at or below is history.
	//
	// `live` alone was the test, and it lost a race that the web makes easy to hit: a describe view
	// auto-sends its opening message the instant the agent appears, so the message landed ~300ms
	// after our online post — already in the database by the time our stream connected. It arrived
	// in the initial replay, BEFORE `: ready`, was filed as backlog, and the agent then sat waiting
	// forever for a message it had already been handed. The id comparison closes it without
	// re-answering history, and survives a restart because each process posts a fresh, higher marker.
	joinID   int64
	pending  []api.PartyMsg // wake triggers awaiting debounce
	pingpong int            // consecutive agent messages since the last human message
	yielded  bool           // suppressed-by-pingpong note already posted this streak
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
			if (r.live || (r.joinID > 0 && ev.msg.ID > r.joinID)) && r.shouldWake(ev.msg) {
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

// activityFeed batches an agent's live step output (tool calls + prose) and posts it to the party
// activity feed (S1), so the web shows what the agent is doing DURING the turn instead of a silent
// "is working ●●●". Best-effort telemetry — a dropped batch just drops a few lines, never the turn.
type activityFeed struct {
	pc   *api.PartyClient
	name string
	mu   sync.Mutex
	buf  []api.PartyActivityLine
	seq  int64
	// Running token tally for this turn. Emitted as a `usage` line on each flush so the web's
	// working indicator can show generation actually happening — a wave says "alive", a rising
	// token count says "producing". Latest line wins; the web ignores all but the last.
	inTok, outTok int
	usageDirty    bool
}

func (a *activityFeed) setUsage(in, out int) {
	a.mu.Lock()
	// Monotonic: events report per-message usage, and a later message must never make the turn look
	// like it went backwards.
	if in > a.inTok {
		a.inTok = in
	}
	if out > a.outTok {
		a.outTok = out
	}
	a.usageDirty = true
	a.mu.Unlock()
}

func (a *activityFeed) add(body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	a.mu.Lock()
	a.seq++
	a.buf = append(a.buf, api.PartyActivityLine{Seq: a.seq, Stream: "step", Body: body})
	a.mu.Unlock()
}

// flush ships whatever's buffered. Called on a timer during the turn (live) and once at the end.
func (a *activityFeed) flush() {
	a.mu.Lock()
	if len(a.buf) == 0 && !a.usageDirty {
		a.mu.Unlock()
		return
	}
	lines := a.buf
	a.buf = nil
	if a.usageDirty {
		a.seq++
		lines = append(lines, api.PartyActivityLine{
			Seq: a.seq, Stream: "usage",
			Body: fmt.Sprintf(`{"in":%d,"out":%d}`, a.inTok, a.outTok),
		})
		a.usageDirty = false
	}
	a.mu.Unlock()
	_ = a.pc.AppendActivity(a.name, lines) // best-effort; never blocks/fails the turn
}

// wake spawns a single fresh agent turn seeded from the transcript + the triggers,
// then posts the reply back to the channel as this agent. With the built-in claude
// engine it streams the agent's ACTIVITY (tool calls + prose) to the operator's terminal AND to the
// party's live activity feed as it happens — so what the agent does is visible and auditable, not secret.
func (r *partyRunner) wake(ctx context.Context, trig []api.PartyMsg) {
	start := time.Now()

	// Re-read the party's persona before every turn (C2, docs/epics/chat-transports.md).
	//
	// This used to be fetched ONCE at connect, which baked the persona into a running process: a
	// human or the agent itself switching context (POST /parties/:id/context) changed the row and
	// nothing downstream noticed, so the only way to change persona was to end the conversation and
	// lose the transcript. The preamble was ALREADY injected per-turn — only the fetch was in the
	// wrong place — so this is a one-line move rather than a redesign.
	//
	// Best-effort by design: a transient failure keeps the persona we already had, which is strictly
	// better than dropping the turn or falling back to a generic assistant mid-conversation.
	if info, e := r.pc.Info(); e == nil && strings.TrimSpace(info.SystemPrompt) != "" {
		if info.SystemPrompt != r.systemPrompt {
			fmt.Printf("   ⇄ context switched → %s\n", info.Mode)
		}
		r.systemPrompt = info.SystemPrompt
	}

	// Attachments: if the human attached files to a trigger message, pull them into a temp dir so the
	// agent can Read them this turn, and note their local paths in the prompt. Best-effort — a failed
	// download just omits that file (we never block the turn). NO type filtering: the agent decides.
	attachNote := ""
	if atts := gatherAttachments(trig); len(atts) > 0 {
		if dir, err := os.MkdirTemp("", "ptln-att-"); err == nil {
			defer os.RemoveAll(dir)
			var lines []string
			for i, a := range atts {
				dest := filepath.Join(dir, fmt.Sprintf("%d-%s", i, attachSafeName(a.name)))
				if err := r.pc.DownloadAttachment(a.id, dest); err != nil {
					fmt.Fprintf(os.Stderr, "   ✗ attachment %q: %v\n", a.name, err)
					continue
				}
				ct := a.contentType
				if ct == "" {
					ct = "unknown type"
				}
				lines = append(lines, fmt.Sprintf("- %s (%s) → %s", a.name, ct, dest))
				fmt.Printf("   📎 pulled %s → %s\n", a.name, dest)
			}
			if len(lines) > 0 {
				attachNote = "\nATTACHED FILES — the human attached these to their message; they are on the local " +
					"filesystem at the paths below. Read them with your tools before replying. If a type isn't usable, " +
					"say so plainly:\n" + strings.Join(lines, "\n") + "\n"
			}
		}
	}

	prompt := r.buildPrompt(trig, attachNote)

	var out string
	var err error
	// Live activity: mirror each step to the operator's terminal AND to the party feed (web tails it
	// over Realtime). A background ticker flushes the buffer during the turn so it streams, not dumps.
	feed := &activityFeed{pc: r.pc, name: r.name}
	stopFlush := make(chan struct{})
	go func() {
		t := time.NewTicker(600 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopFlush:
				return
			case <-t.C:
				feed.flush()
			}
		}
	}()
	defer func() {
		close(stopFlush)
		feed.flush() // final drain — whatever streamed after the last tick
	}()
	onUsage := func(in, out int) { feed.setUsage(in, out) }
	onActivity := func(activity string) {
		fmt.Printf("   %s\n", activity) // live to the operator's terminal
		feed.add(activity)              // live to the party's web activity feed
	}
	switch {
	case r.customCmd != "":
		fmt.Printf("\n↯ %s waking (%s)…\n", r.name, r.customCmd)
		out, err = runAgentPlain(ctx, append(splitFields(r.customCmd), r.passthru...), prompt, "", true, false, nil, r.idleTimeout, r.maxTimeout, onActivity, onUsage)
	default:
		spec, _ := eng.Lookup(r.engine) // membership validated at startup (partyAgentMain)
		label := r.engine
		if r.model != "" {
			label += " " + r.model
		}
		fmt.Printf("\n↯ %s waking (%s)…\n", r.name, label)
		argv := append([]string{spec.Bin}, spec.Args(r.model, r.passthru)...)
		// Embody an existing session: resume it (forked, so the original is never
		// touched) so the agent carries its real context. claude-only for now.
		if r.engine == "claude" && r.resumeID != "" {
			argv = append([]string{spec.Bin, "--resume", r.resumeID, "--fork-session"}, spec.Args(r.model, r.passthru)...)
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
		out, err = runAgentPlain(ctx, argv, prompt, r.resumeDir, spec.Stdin, spec.Stream, env, r.idleTimeout, r.maxTimeout, onActivity, onUsage)
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

// msgAttachment is a file a human attached to a message (from meta.attachments).
type msgAttachment struct {
	id, name, contentType string
}

// gatherAttachments pulls the attachment refs out of the trigger messages' meta (dedup by id, preserving
// order). These are the files the human just attached, which the runner downloads for the agent to Read.
func gatherAttachments(trig []api.PartyMsg) []msgAttachment {
	var out []msgAttachment
	seen := map[string]bool{}
	for _, m := range trig {
		raw, ok := m.Meta["attachments"].([]any)
		if !ok {
			continue
		}
		for _, r := range raw {
			mp, ok := r.(map[string]any)
			if !ok {
				continue
			}
			a := msgAttachment{}
			if s, ok := mp["id"].(string); ok {
				a.id = s
			}
			if s, ok := mp["name"].(string); ok {
				a.name = s
			}
			if s, ok := mp["content_type"].(string); ok {
				a.contentType = s
			}
			if a.id == "" || seen[a.id] {
				continue
			}
			seen[a.id] = true
			out = append(out, a)
		}
	}
	return out
}

// attachSafeName reduces a client filename to a safe base name for the temp dir.
func attachSafeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "file"
	}
	return name
}

func (r *partyRunner) buildPrompt(trig []api.PartyMsg, attachNote string) string {
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
	// Project context: the party's linked Context Thread canon (decisions/constraints/contracts the
	// team recorded). Injected as background the planning agent already knows — the same "every agent
	// starts warm with project context" invariant as the mux primer + crank workerContext. Fetched
	// once (best-effort) so a per-turn network call / a missing thread never slows or blocks a turn.
	if ctx := r.projectContext(); ctx != "" {
		b.WriteString("\nPROJECT CONTEXT — shared team facts on this thread (background you already know; " +
			"do NOT act on them unless asked):\n----- context -----\n" + ctx + "\n----- end context -----\n")
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
	if attachNote != "" {
		b.WriteString(attachNote)
	}
	fmt.Fprintf(&b, "\nReply now as %s (or reply with nothing if there's nothing useful to add):\n", r.name)
	return b.String()
}

// ----- helpers -----

// grounded reports whether a party turn runs in cited-position (evidence) mode — RESEARCH, then
// answer only inside verified `position` blocks. It's server-authoritative: the control plane sends
// `override` per party so flipping grounding is a web deploy, not a daemon release. Precedence:
//  1. describe is NEVER grounded — grounding replaces its propose_edit/question facilitation loop
//     with "no prose outside position blocks", so the agent posts one cited claim and the interview
//     dies with an empty doc (the bug that started all this). This wins over any server value.
//  2. an explicit server `override` (the normal path once the backend sends it).
//  3. the daemon's own default for an older backend that sends nothing: approach-review grounds,
//     plus a manual `--evidence` flag.
func grounded(override *bool, evidenceFlag bool, partyMode string) bool {
	if partyMode == "describe" {
		return false
	}
	if override != nil {
		return *override
	}
	return evidenceFlag || partyMode == "approach"
}

// clampTimeout resolves a server-sent timeout (seconds, nil = unset) to a duration, bounded to
// [lo, hi] so a missing, zero, or hostile value can never disable the turn guard. Unset → def.
func clampTimeout(sec *int, def, lo, hi time.Duration) time.Duration {
	if sec == nil {
		return def
	}
	d := time.Duration(*sec) * time.Second
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// resolvePartyModel decides the model an engine turn runs with: an explicit --model wins,
// then the party's server-configured model (settings.model — the project's plan model, for
// EVERY engine), then the engine's own default. The server value is REMOTE input headed for
// an exec argv, so it's shape-gated by modelRe (daemon.go) — an invalid token is ignored,
// never passed through. Only claude gets a hard default (the party's cheap "haiku" alias is
// claude-specific); other engines run their host default when empty (no --model flag).
func resolvePartyModel(engine, cliModel string, modelSet bool, serverModel string) string {
	m := cliModel
	if !modelSet && serverModel != "" {
		if modelRe.MatchString(serverModel) {
			m = serverModel
		} else {
			fmt.Fprintf(os.Stderr, "ptln party: ignoring party model %q (not a plain model token)\n", serverModel)
		}
	}
	if engine == "claude" && m == "" {
		m = "haiku"
	}
	return m
}

// Engine invocation (bin, args builder, stdin/stream behavior) lives in
// internal/engine — the ONE registry every agent exec path shares. Adding a new
// engine = one entry in that package's specs map (the lowest common denominator
// is plain text-in/text-out, which `--cmd` already covers for anything not listed).

func splitFields(s string) []string { return strings.Fields(s) }

// projectContext returns the party's linked-thread canon (decisions/constraints/contracts), fetched
// ONCE and cached, framed for injection as background — the planning-agent side of "every partyline
// agent starts with project context". Best-effort like the mux primer: any failure (no thread, no
// token, network) yields "" and is not retried, so a turn is never slowed or blocked by it.
func (r *partyRunner) projectContext() string {
	if r.threadCtxSet {
		return r.threadCtx
	}
	r.threadCtxSet = true // resolve at most once, success or not
	if api.LoadToken() == "" {
		return ""
	}
	tree, err := r.pc.PlanRead()
	if err != nil || tree == nil || tree.ThreadID == "" {
		return ""
	}
	_, blocks, err := api.New().GetThread(tree.ThreadID)
	if err != nil || len(blocks) == 0 {
		return ""
	}
	facts := formatContextBlocks(blocks) // shared with the primer; hides superseded/pruned/proposed
	if strings.TrimSpace(facts) == "" || strings.HasPrefix(facts, "No shared context") {
		return ""
	}
	r.threadCtx = facts
	return r.threadCtx
}

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
func runAgentPlain(ctx context.Context, argv []string, prompt, dir string, stdin, stream bool, env []string, idle, backstop time.Duration, onActivity func(string), onUsage func(in, out int)) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("no command")
	}
	// Turn deadline (idle + max come from the party's server-driven, clamped settings). A turn is
	// killed only when it's genuinely STUCK, not merely slow:
	//   - stream engines (claude): an *idle* timeout — reset on every event the engine
	//     emits (tool_use / text / any line). A turn actively streaming tool calls never
	//     times out; only silence for `idle` kills it. `max` is an absolute backstop so a
	//     runaway can't run forever.
	//   - non-stream engines: no progress to observe, so a plain wall-clock (`max`).
	// (Was a flat 120s wall-clock, which guillotined legitimate multi-step research turns —
	// e.g. a decompose turn spawning a subagent + dozens of tool calls — mid-work.)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	hardStop := time.AfterFunc(backstop, func() { timedOut.Store(true); cancel() })
	defer hardStop.Stop()
	var idleStop *time.Timer
	if stream {
		idleStop = time.AfterFunc(idle, func() { timedOut.Store(true); cancel() })
		defer idleStop.Stop()
	}
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
		if timedOut.Load() {
			if stream {
				return fmt.Errorf("agent stalled — no output for %s", idle)
			}
			return fmt.Errorf("agent timed out after %s", backstop)
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
			idleStop.Reset(idle) // progress — the agent isn't stuck; extend the idle deadline
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
								// Stream prose blocks to the activity feed too (not just tool calls), so the
								// web shows the agent's thinking forming — deduped against the previous block.
								if onActivity != nil && s != strings.TrimSpace(lastText) {
									onActivity(s)
								}
								lastText = b.Text
							}
						}
					}
					if onUsage != nil && ev.Message.Usage.OutputTokens > 0 {
						onUsage(ev.Message.Usage.InputTokens, ev.Message.Usage.OutputTokens)
					}
				case "result":
					if ev.Result != "" {
						finalText = ev.Result
					}
					if onUsage != nil && ev.Usage.OutputTokens > 0 {
						onUsage(ev.Usage.InputTokens, ev.Usage.OutputTokens)
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
		// Token usage rides on assistant messages; the terminal `result` event carries the final
		// tally. Surfaced so the party's working indicator can show the turn actually GENERATING
		// rather than just spinning — a spinner tells you a process is alive, a rising token count
		// tells you it's doing something.
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
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
  --context-file <f>  seed the agent with a briefing (e.g. a session summary) instead
           of raw channel history — cheaper and more robust than --resume
  --evidence  grounded cited-position mode: claims must carry citations that an
           independent check verifies before they post (default for approach-review)
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
