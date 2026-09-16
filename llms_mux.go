package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/brand"
	eng "partyline.sh/partyline/internal/engine"
	"partyline.sh/partyline/internal/gitwt"
	"partyline.sh/partyline/internal/ptymux"
)

// startThreadCheckup is the Common Ground post-gap checkup (COMMON-GROUND §12.4). When the
// session is attached to a thread (PARTYLINE_THREAD_ID, set by `ptln new --thread`), it polls
// for shared-context updates and — only when you've been idle past the gap threshold and there
// is unseen news — shows a one-line banner in the mux. It NEVER injects into the agent
// (system-initiated delivery is informational only, §5); you act on it by asking your agent,
// which has the `recall` tool. The watermark starts at the latest block (the at-launch primer
// already delivered everything up to launch), so the banner only ever reports what's genuinely
// new since you stepped away. Idle threshold defaults to 30m; PARTYLINE_CHECKUP_IDLE_SECS
// overrides it (handy for testing — set it to e.g. 30).
// checkupTarget is what the thread checkup needs from whichever backend hosts the sessions:
// whose thread to watch, whether the human is around, and a banner that won't stomp another.
type checkupTarget interface {
	ActiveThread() string
	IdleSince() time.Duration
	BannerActive() bool
	SetBanner(string)
}

func startThreadCheckup(mx checkupTarget) {
	if api.LoadToken() == "" {
		return
	}
	// The watched thread is whatever the FOCUSED session is attached to — set at launch
	// (ptln new --thread) or later via ctrl-\ c. So attaching a thread mid-session (no relaunch)
	// starts the checkup, and switching sessions follows the one you're looking at.
	idle := 30 * time.Minute
	if s := strings.TrimSpace(os.Getenv("PARTYLINE_CHECKUP_IDLE_SECS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			idle = time.Duration(n) * time.Second
		}
	}
	poll := 90 * time.Second
	if s := strings.TrimSpace(os.Getenv("PARTYLINE_CHECKUP_POLL_SECS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			poll = time.Duration(n) * time.Second
		}
	}
	c := api.New()
	go func() {
		// A change is any web/agent edit: a new block, OR a status flip on an existing one
		// (accept, prune, revert, promote). We fingerprint the whole feed as "id:status" so
		// in-place flips are caught too, not just appends. `baseline` is snapshotted the moment
		// you step away, so the banner only ever reports what changed *since you stepped away*.
		armed := true // one banner per away-period; re-armed whenever you're active again
		active := true
		baseline := ""
		curThread := "" // the thread we're currently baselined against
		for {
			time.Sleep(poll)
			// Follow the focused session's attached thread (fallback: the launch env). Empty →
			// nothing attached to watch. A change of thread (switch/attach) re-baselines.
			thread := mx.ActiveThread()
			if thread == "" {
				thread = strings.TrimSpace(os.Getenv("PARTYLINE_THREAD_ID"))
			}
			if thread == "" {
				curThread, baseline = "", ""
				continue
			}
			if thread != curThread {
				curThread, baseline, active = thread, "", true
			}
			if mx.IdleSince() < idle { // you're around → nothing to surface; re-arm for the next gap
				armed, active = true, true
				continue
			}
			if active { // just stepped away → mark the baseline; report only later changes
				if bl, err := c.Recall(thread, 0); err == nil {
					baseline = feedFingerprint(bl)
				}
				active = false
				continue
			}
			if !armed || mx.BannerActive() {
				continue
			}
			bl, err := c.Recall(thread, 0) // only fetched while you're away
			if err != nil {
				continue
			}
			cur := feedFingerprint(bl)
			if cur == baseline {
				continue
			}
			n := feedChangeCount(bl, baseline)
			baseline = cur
			if n == 0 {
				continue
			}
			plural := ""
			if n != 1 {
				plural = "s"
			}
			mx.SetBanner(fmt.Sprintf("▲ %d update%s to your team's shared context since you stepped away — ask your agent \"what changed?\"", n, plural))
			armed = false
		}
	}()
}

// feedFingerprint reduces the feed to a stable "id:status;" string so the checkup can detect any
// change — a new block or an in-place status flip (accept/prune/revert/promote).
func feedFingerprint(bl []api.ContextBlock) string {
	var sb strings.Builder
	for _, b := range bl {
		fmt.Fprintf(&sb, "%d:%s;", b.ID, b.Status)
	}
	return sb.String()
}

// feedChangeCount counts blocks that are new or changed vs a baseline fingerprint. It ignores
// blocks that merely became 'superseded' — that's the shadow of a replacement already counted by
// the new revision — so a single edit reads as "1 update", not two.
func feedChangeCount(bl []api.ContextBlock, baseline string) int {
	seen := map[string]bool{}
	for _, tok := range strings.Split(baseline, ";") {
		if tok != "" {
			seen[tok] = true
		}
	}
	n := 0
	for _, b := range bl {
		if b.Status == "superseded" {
			continue
		}
		if !seen[fmt.Sprintf("%d:%s", b.ID, b.Status)] {
			n++
		}
	}
	return n
}

// launcherEngineBin resolves a tool name (as shown in the launcher) to its executable via
// the internal/engine registry. `agy` is accepted as an alias for antigravity. This is the
// one place new-session launching and the tool list agree on what to exec. "" = unknown.
func launcherEngineBin(tool string) string {
	name := strings.ToLower(tool)
	if name == "agy" {
		name = "antigravity"
	}
	spec, ok := eng.Lookup(name)
	if !ok {
		return ""
	}
	return spec.Bin
}

// isWireableEngine reports whether an executable (a child's argv[0]) is an AI engine we can wire
// Common Ground into by (re)launching it — vs. a plain shell, which can only record-as-you.
func isWireableEngine(bin string) bool {
	switch bin {
	case "claude", "codex", "gemini", "agy":
		return true
	}
	return false
}

// mcpManagedClaudeConfig reports whether a claude --mcp-config value is one partyline wrote —
// it names the context-threads server (current or legacy) or a catalog server. Anything else
// is the user's own flag and survives strip-and-rebuild untouched.
func mcpManagedClaudeConfig(val string, cat mcpCatalog) bool {
	if strings.Contains(val, `"partyline-context-threads"`) || strings.Contains(val, `"common-ground"`) {
		return true
	}
	for n := range cat {
		if strings.Contains(val, `"`+n+`"`) {
			return true
		}
	}
	return false
}

// stripSessionWiring removes every partyline-managed wiring flag from a launch argv — the
// claude --mcp-config/--append-system-prompt we wrote and codex -c mcp_servers.* overrides for
// managed names. Keeps --resume/--continue (the conversation) and any user-supplied flags.
func stripSessionWiring(argv []string, cat mcpCatalog) []string {
	out := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--append-system-prompt" {
			i++ // ours (the thread primer) — drop flag AND value
			continue
		}
		if a == "--mcp-config" && i+1 < len(argv) && mcpManagedClaudeConfig(argv[i+1], cat) {
			i++
			continue
		}
		if a == "-c" && i+1 < len(argv) && mcpManagedCodexOverride(argv[i+1], cat) {
			i++
			continue
		}
		out = append(out, a)
	}
	return out
}

// carryConversation makes a relaunched claude pick up the conversation it was in (best-effort
// --continue). codex/gemini have no per-launch equivalent — their argv passes through.
func carryConversation(bin string, argv []string) []string {
	if bin != "claude" {
		return argv
	}
	for _, a := range argv {
		if a == "--resume" || a == "--continue" {
			return argv
		}
	}
	return append(append([]string(nil), argv...), "--continue")
}

// inheritRepoBindSpec makes REOPENED sessions honor the repo bind (.partyline.json), exactly like
// `ptln new` does: a spec with no thread whose Dir sits in a bound repo gets the bound thread wired
// in (MCP + primer via wireSessionArgv) before launch. Without this, closing and reopening a session
// silently dropped its context wiring — the founder's "this has to work with existing sessions"
// requirement. A spec that already carries a thread is untouched; --no-thread isn't a concept here
// because a saved spec that opted out simply carries no Dir-bind relationship the user wants kept.
func inheritRepoBindSpec(spec ptymux.Spec) ptymux.Spec {
	if spec.Thread != "" || len(spec.Argv) == 0 {
		return spec
	}
	bound := ""
	if spec.Dir != "" {
		bound = loadRepoBind(spec.Dir)
	}
	bin := filepath.Base(spec.Argv[0])
	if !isWireableEngine(bin) {
		// gemini/antigravity/shells: no per-launch wiring — the thread still rides the
		// spec → env (may be empty; nothing to wire either way).
		spec.Thread = bound
		return spec
	}
	// Zero-config MCP: wire even with NO bound thread. The context server must exist in every
	// engine session the moment the CLI runs ("available the very second they run the CLI");
	// cg-mcp resolves the thread lazily at call time, so bind-then-use works in ANY order —
	// including a repo bound while the session is already open.
	argv, engine := wireSessionArgv(bin, spec.Argv, bound, spec.MCPs)
	spec.Argv, spec.Engine, spec.Thread = argv, engine, bound
	return spec
}

// wireSessionArgv rebuilds a child's launch argv from desired state: the Common Ground thread
// (recall/remember + primer) plus the selected catalog MCP servers. Always strip-then-rebuild —
// never incremental — so toggles are idempotent and the argv can't accumulate stale wiring.
// The thread/engine ride the SPEC (→ the child's own env via childEnv), never a global —
// that's the fix for cross-session contamination. Returns the wired argv + engine name.
func wireSessionArgv(bin string, argv []string, thread string, mcps []string) ([]string, string) {
	engine := bin
	if bin == "agy" {
		engine = "antigravity"
	}
	cat := loadMCPCatalog()
	argv = stripSessionWiring(argv, cat)
	switch bin {
	case "claude":
		// ONE merged --mcp-config: context-threads (ALWAYS — zero-config: the tools exist the
		// moment the CLI runs; the thread resolves lazily in cg-mcp when unbound) + every
		// selected catalog server. Registered alongside the agent's own servers (no
		// --strict-mcp-config).
		if cfg := mcpServersJSON(true, mcps, cat); cfg != "" {
			argv = append(argv, "--mcp-config", cfg)
		}
		if thread != "" {
			// At-launch primer (COMMON-GROUND §12.3): the thread's current shared context goes
			// into the system prompt so the agent starts already knowing it. Best-effort — a
			// slow/failed fetch never blocks the (re)launch.
			if primer := threadPrimer(thread); primer != "" {
				argv = append(argv, "--append-system-prompt", primer)
			}
		}
	case "codex":
		// codex takes per-invocation MCP config via -c (TOML dotted overrides). No
		// system-prompt flag → no auto-primer; the agent uses recall itself. context-threads
		// rides every launch (zero-config) — the thread resolves lazily in cg-mcp when unbound.
		exe := selfExe()
		argv = append(argv, "-c", fmt.Sprintf("mcp_servers.partyline-context-threads.command=%q", exe), "-c", `mcp_servers.partyline-context-threads.args=["cg-mcp"]`)
		argv = append(argv, mcpCodexFlags(mcps, cat)...)
		// gemini / antigravity: no per-invocation MCP hook — they use a one-time persistent
		// registration (`ptln thread connect`, or the engine's own `mcp add`). The thread
		// still rides the child's env once they're wired.
	}
	return argv, engine
}

// threadPrimer fetches a thread's current shared context and frames it as a system-prompt
// addition: background the agent already knows, explicitly NOT a to-do list. Best-effort —
// returns "" on any error (no thread context, not logged in, network) so launch never blocks.
func threadPrimer(threadID string) string {
	if api.LoadToken() == "" {
		return ""
	}
	_, blocks, err := api.New().GetThread(threadID)
	if err != nil {
		return "" // can't reach the thread → don't block launch; no primer
	}
	// The standing instruction: capture seam-facts AS the work happens (Model A — the session's
	// own LLM records via `remember`; no separate scribe, no partyline cost). Emitted even when
	// the thread is empty, so a fresh thread still tells the agent to start capturing.
	primer := "## Shared team context (partyline Context Threads)\n\n" +
		"This thread is the team's shared memory across people, machines, and tools. Two jobs:\n\n" +
		"1. Treat the context below as background you already know. Do NOT act on it or change " +
		"anything because of it unless the user asks.\n" +
		"2. KEEP IT CURRENT AS YOU WORK. The moment a real decision is made, you hit a hard " +
		"constraint, or you agree an interface/contract that another person or component will " +
		"depend on, immediately record it with the partyline-context-threads `remember` tool — one concise " +
		"fact, kind = decision | constraint | contract, tagged with `entities` (1-3 slugs naming " +
		"what it's about; reuse the slugs listed below). Record only durable, cross-seam facts " +
		"(not chatter, routine steps, or things scoped to this session). If a new fact overturns " +
		"an earlier one, `remember` it with `supersedes` set. Pull the latest with `recall` before " +
		"you rely on shared context — `recall {entity}` scopes it to one thing.\n\n"
	if len(blocks) == 0 {
		return primer + "No shared context recorded yet — you're starting it."
	}
	return primer + formatContextBlocks(blocks) // shared with cg-mcp; hides superseded/pruned/proposed
}

// llmsNew starts a FRESH session of an engine in the mux — the new-session counterpart to
// resuming. `ptln llms new <claude|codex|gemini|antigravity> [dir] [--thread <id>]`.
// --thread attaches the session to a context thread: the thread id rides the env
// (the mux child inherits it), so the engine's cg-mcp server reads/writes that thread.
func llmsNew(args []string) {
	var pos []string
	thread, worktree, goal := "", "", ""
	noThread, withState := false, false
	keepGoing := 0
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--thread":
			if i++; i < len(args) {
				thread = strings.TrimSpace(args[i])
			}
			continue
		case "--worktree", "--wt":
			if i++; i < len(args) {
				worktree = strings.TrimSpace(args[i])
			}
			continue
		case "--no-thread":
			noThread = true
			continue
		case "--with-state":
			withState = true
			continue
		case "--keep-going":
			if i++; i < len(args) {
				keepGoing, _ = strconv.Atoi(strings.TrimSpace(args[i]))
			}
			continue
		case "--goal":
			if i++; i < len(args) {
				goal = args[i]
			}
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) == 0 {
		fatal(fmt.Errorf("usage: ptln llms new <claude|codex|gemini|opencode|goose|antigravity> [dir] [--thread <id>] [--worktree <name> [--with-state]] [--no-thread]"))
	}
	dir, _ := os.Getwd()
	if len(pos) > 1 && strings.TrimSpace(pos[1]) != "" {
		if abs, err := filepath.Abs(pos[1]); err == nil {
			dir = abs
		}
	}
	spec, err := newSessionSpec(pos[0], dir, thread, worktree, goal, withState, noThread, keepGoing)
	if err != nil {
		fatal(err)
	}
	if err := runLLMSApp([]ptymux.Spec{spec}); err != nil {
		fatal(err)
	}
}

// bypassFlagsFor is the engine's own skip-permissions mode, from the same table the share picker
// uses — one definition of what "bypass" means per engine, so a new engine added there gets the
// ctrl-n behaviour without a second edit.
func bypassFlagsFor(tool string) []string {
	for _, m := range permissionModes(tool) {
		if m.danger {
			return m.flags
		}
	}
	return nil
}

// quickNewEngine is what 'N' launches: PARTYLINE_ENGINE when it names a known engine, else
// claude. Env-driven so a codex-first person gets their engine on the same key.
func quickNewEngine() string {
	e := strings.TrimSpace(os.Getenv("PARTYLINE_ENGINE"))
	switch e {
	case "claude", "codex", "gemini", "antigravity":
		return e
	}
	return "claude"
}

// newSessionSpec builds the launch Spec for a fresh AI session — the ONE path shared by
// `ptln new` (CLI) and the ctrl-\ n New/Run menu, so both start sessions identically:
// resolve the engine, optionally create+seed a worktree (with optional with-state fork),
// inherit the repo's bound thread (E3.5), wire the thread MCP + primer, and arm keep-going.
// Side effects (worktree, keep-going state) are intentional. Returns an error instead of
// exiting so the menu can surface it inline. Progress notes go to stderr (harmless in the mux).
func newSessionSpec(tool, dir, thread, worktree, goal string, withState, noThread bool, keepGoing int, permFlags ...string) (ptymux.Spec, error) {
	bin := launcherEngineBin(tool)
	if bin == "" {
		return ptymux.Spec{}, fmt.Errorf("unknown tool %q — try: claude, codex, gemini, opencode, goose, antigravity", tool)
	}
	if _, err := exec.LookPath(bin); err != nil {
		return ptymux.Spec{}, fmt.Errorf("%s not found on PATH — is it installed?", bin)
	}
	label := bin
	if worktree != "" {
		repo, err := gitwt.RepoRoot(dir)
		if err != nil {
			return ptymux.Spec{}, fmt.Errorf("--worktree needs a git repository: %w", err)
		}
		wtPath, branch, err := gitwt.Create(repo, worktree)
		if err != nil {
			return ptymux.Spec{}, err
		}
		_ = gitwt.SeedInclude(repo, wtPath)
		if withState {
			if err := gitwt.MaterializeWIP(dir, wtPath); err != nil {
				return ptymux.Spec{}, fmt.Errorf("--with-state: %w", err)
			}
		}
		fmt.Fprintf(os.Stderr, "⎇ worktree %s (branch %s)\n", wtPath, branch)
		dir = wtPath
		label = bin + " ⎇" + branch
	}
	// E3.5 — inherit the repo's bound thread when none was given (unless opted out).
	if thread == "" && !noThread {
		if bound := loadRepoBind(dir); bound != "" {
			thread = bound
			fmt.Fprintf(os.Stderr, "☎ using this repo's context thread (.partyline.json)\n")
		}
	}
	argv := []string{bin}
	if thread != "" {
		// thread/engine ride the SPEC → the child's own env (no global; the contamination fix).
		argv, _ = wireSessionArgv(bin, argv, thread, nil)
		if bin != "claude" && bin != "codex" {
			fmt.Fprintf(os.Stderr, "note: %s needs a one-time `ptln thread connect %s` (thread rides the env).\n", bin, strings.ToLower(tool))
		}
	}
	// E4.0 keep-going: a Stop hook auto-continues up to N turns / until the done sentinel. Hard-capped.
	if keepGoing > 0 {
		if bin != "claude" {
			fmt.Fprintf(os.Stderr, "note: --keep-going is claude-only for now — ignored for %s.\n", bin)
		} else if key, err := armKeepGoing(keepGoing, goal); err == nil {
			argv = append(argv, "--settings", keepGoingSettings(key))
			fmt.Fprintf(os.Stderr, "⏩ keep-going armed: up to %d continuations\n", keepGoing)
		}
	}
	// Permission flags go LAST: they are the operator's explicit word on this launch and must
	// win over anything the wiring appended.
	argv = append(argv, permFlags...)
	threadLabel, engine := "", ""
	if thread != "" {
		engine = strings.ToLower(tool)
		if th, _, err := api.New().GetThread(thread); err == nil && th != nil {
			threadLabel = th.Title
		}
	}
	return ptymux.Spec{
		Label:       label,
		Key:         fmt.Sprintf("new-%s-%d", bin, time.Now().UnixNano()),
		Argv:        argv,
		Dir:         dir,
		Thread:      thread,
		ThreadLabel: threadLabel,
		Engine:      engine,
	}, nil
}

// firstLauncherRun reports whether this is the launcher's very first open on this machine —
// and stamps the marker so it's true exactly once (E6.3: the welcome footer). Best-effort:
// any filesystem trouble reads as "not first" so onboarding can never break the launcher.
func firstLauncherRun() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	p := filepath.Join(home, ".partyline", "onboarded")
	if _, err := os.Stat(p); err == nil {
		return false
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return false
	}
	return os.WriteFile(p, []byte("1\n"), 0o600) == nil
}

// tmuxAfterMenu wraps a Suspend-run menu so a session it queued (SetPendingOpen) opens in
// tmux — drained inside the same suspend, before the mux's own drain would spawn it as an
// in-process child. A no-op wrapper when the backend is off.
func tmuxAfterMenu(mx *ptymux.Mux, fn func()) func() {
	return func() {
		fn()
		if !useTmuxBackend() {
			return
		}
		if sp := mx.TakePendingOpen(); sp != nil {
			if err := runTmuxApp([]ptymux.Spec{*sp}); err != nil {
				fmt.Fprintf(os.Stderr, "ptln: %v\n", err)
			}
		}
	}
}

// llmsHome adapts the aiMenu (the `ptln llms` browser) to the ptymux.Home interface, so
// the mux can host it as the persistent launcher screen. The mux owns the terminal; this
// adapter just renders the menu and translates its keys into mux actions.
type llmsHome struct {
	m   *aiMenu
	mux *ptymux.Mux
	// inPane marks this instance as one of the two managers hosted INSIDE a guided split
	// setup (newPaneHome). Such an instance exists only while that setup is on screen, which
	// is what makes esc unconditional there — see HandleKey.
	inPane bool
}

func (h *llmsHome) Render(cols, rows int) {
	h.m.w, h.m.h = cols, rows
	if h.m.w < 40 || h.m.h < 10 {
		h.m.w, h.m.h = 80, 24
	}
	h.syncLive()
	h.m.clampScroll() // the height is only real HERE; clampScroll is a no-op until it is
	h.m.render()
}

// syncLive marks the sessions currently running in the mux and, when that set changed,
// re-sorts so live sessions float to the top — preserving the highlighted session across
// the reorder so the cursor doesn't move. Called from Enter() too (before the FIRST paint),
// so the launcher opens already in final order instead of rendering once then jumping.
func (h *llmsHome) syncLive() {
	if h.mux == nil {
		return
	}
	live := h.mux.LiveKeys()
	if sameLive(h.m.live, live) {
		return
	}
	var sel string
	if s := h.m.selected(); s != nil {
		sel = s.ID
	}
	h.m.live = live
	h.m.applyFilter()
	if sel != "" {
		h.m.selectByID(sel)
	}
}

// markedSpecs builds open-specs for the sessions selected with space, in display order.
// They open with default permissions (the bare resume argv) — open one on its own (⏎ with
// nothing selected) if you want to pick a permission mode for it.
func (h *llmsHome) markedSpecs() []ptymux.Spec {
	m := h.m
	var specs []ptymux.Spec
	for i := range m.view {
		s := m.view[i]
		if !m.marked[s.ID] || s.resumeArgv == nil {
			continue
		}
		m.markUsed(s.ID)
		specs = append(specs, inheritRepoBindSpec(ptymux.Spec{Label: muxLabelFor(s, m.meta), Key: s.ID, Model: sessionModel(s), Argv: s.resumeArgv, Dir: s.resumeDir}))
	}
	return specs
}

// sameLive reports whether two live-session sets are equal (both hold only true values).
func sameLive(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// Enter resets transient view state when the mux returns to the launcher, so coming back
// from a session always shows the full list — a lingering search filter was hiding sessions
// (you'd see only the matches from before you opened one). Selection + sort/theme persist.
func (h *llmsHome) Enter() {
	m := h.m
	// Dismiss any transient modal/intent left open in a prior visit (permission picker,
	// rename, share) so returning via ctrl-o lands on a clean launcher — not a stale picker.
	m.picking, m.pickArmed, m.pickCustom = false, false, false
	m.renaming, m.renameBuf = false, ""
	m.sharing = false
	m.confirmInstall = false
	if m.query != "" || m.filter {
		m.filter, m.query = false, ""
		m.applyFilter()
	}
	// Re-scan the session stores on every return to the launcher, so sessions started
	// (or ended) while you were inside one show up without a manual 'R'. refresh() keeps
	// the highlight stable by id and re-applies the (now-reset) filter.
	m.refresh()
	h.syncLive() // float live sessions to the top BEFORE the first paint (no open-time jump)
}

// HandleKey wraps the launcher's real key handling with the tmux handoff: when the backend
// is on, a Spawn/SpawnMany action becomes a Suspend that attaches tmux — the mux restores
// the terminal, the user lives in tmux until detach, and the launcher re-enters (Enter()
// refreshes the list). The built-in mux thus never hosts a live child in tmux mode.
func (h *llmsHome) HandleKey(b []byte) ptymux.HomeAction {
	act := h.handleKeyInner(b)
	if !useTmuxBackend() {
		return act
	}
	var specs []ptymux.Spec
	switch {
	case act.Spawn != nil:
		specs = []ptymux.Spec{*act.Spawn}
	case len(act.SpawnMany) > 0:
		specs = act.SpawnMany
	default:
		return act
	}
	return ptymux.HomeAction{Suspend: func() {
		if err := runTmuxApp(specs); err != nil {
			fmt.Fprintf(os.Stderr, "ptln: %v\n", err)
		}
	}}
}

func (h *llmsHome) handleKeyInner(b []byte) ptymux.HomeAction {
	m := h.m
	// A consent modal (launch approval, or always-on install) owns input — route straight to
	// the menu so the mux-level esc/enter/S shortcuts below can't hijack y/n/esc.
	if m.apprActive() || m.confirmInstall {
		m.handleKey(b)
		return ptymux.HomeAction{}
	}
	enter := len(b) > 0 && (b[0] == '\r' || b[0] == '\n')
	ready := !m.picking && !m.filter && !m.help && !m.renaming
	// esc (when idle — not searching/renaming/in the detail pane, nothing multi-selected)
	// jumps back to the session you came from. Otherwise esc falls through to the menu (it
	// clears a search / a multi-select / the detail focus). Pairs with ctrl-\ o (→ manager).
	if ready && !m.focusR && len(m.marked) == 0 && len(b) == 1 && b[0] == 0x1b {
		// In a split-setup pane the LiveKeys gate must NOT apply: the setup's own status bar
		// promises "esc cancels", and its documented entry path is a bare `|` with NOTHING
		// live yet — so gating on live sessions killed the exit exactly when it was the only
		// one. Return here means cancelSplit (splitpane.go), which lands on st.origin, or the
		// launcher when the setup was started from the launcher.
		if h.inPane || len(h.mux.LiveKeys()) > 0 {
			return ptymux.HomeAction{Return: true}
		}
	}
	// 'S' shares the highlighted session over the relay; pre-check login so we can say so
	// clearly instead of failing inside the picker or the spawned host.
	if ready && len(b) == 1 && b[0] == 'S' {
		if s := m.selected(); s != nil && s.resumeArgv != nil && api.LoadToken() == "" {
			m.flash = "sign in to share — run: ptln login"
			return ptymux.HomeAction{}
		}
	}
	// ⏎ with rows selected (space) opens them all into one multiplexed terminal at once.
	if enter && ready && len(m.marked) > 0 {
		specs := h.markedSpecs()
		m.marked = nil
		if len(specs) > 0 {
			return ptymux.HomeAction{SpawnMany: specs}
		}
	}
	// ⏎ on an already-live session jumps straight to it — no resume modal, no duplicate.
	if enter && ready {
		if s := m.selected(); s != nil && h.mux.LiveKeys()[s.ID] {
			m.markUsed(s.ID) // jumping back to a live one counts as using it
			return ptymux.HomeAction{SwitchKey: s.ID}
		}
	}
	done, chosen := m.handleKey(b)
	// Keep a live session's mux label in sync with a rename just made in the launcher, so
	// the picker + status bar match the new title (idempotent + a no-op if it's not live).
	if s := m.selected(); s != nil {
		h.mux.Relabel(s.ID, muxLabelFor(*s, m.meta))
	}
	// 'n' opens the full New/Run menu (#160 — E9's last named gap): AI session with options ·
	// blank terminal · ptln work · ptln crank. This is the SAME menu the in-session ctrl-\ n
	// chord opens, surfaced at the front door — the person who most needs a menu instead of
	// flags is the one who just typed bare `ptln`. The plain shell the key used to open is
	// door 2 inside, one keypress deeper.
	if m.spawnShell {
		m.spawnShell = false
		if h.mux.NewFn != nil {
			return ptymux.HomeAction{Suspend: h.mux.NewFn}
		}
		return ptymux.HomeAction{Spawn: shellSpec()} // no menu wired (tests) — old behavior
	}
	// 'N' — a fresh session NOW: default engine, the focused session's directory (else cwd),
	// no thread, no worktree, no questions. Built through the same newSessionSpec as `ptln new`
	// and the menu, so the only difference from door 1 is that nothing is asked.
	if m.quickNew || m.quickNewBypass {
		bypass := m.quickNewBypass
		m.quickNew, m.quickNewBypass = false, false
		dir, _ := os.Getwd()
		if _, d, _, _, ok := h.mux.ActiveLaunch(); ok && d != "" {
			dir = d
		}
		tool := quickNewEngine()
		var perm []string
		if bypass {
			perm = bypassFlagsFor(tool)
		}
		spec, err := newSessionSpec(tool, dir, "", "", "", false, false, 0, perm...)
		if err != nil {
			m.flash = err.Error()
			return ptymux.HomeAction{}
		}
		spec = inheritRepoBindSpec(spec)
		return ptymux.HomeAction{Spawn: &spec}
	}
	// '|' — the guided split. Wired here (the Home layer) so every way into the manager gets
	// it: bare `ptln`, ctrl-\ o, and the welcome screen's "find a session" door.
	if m.splitSetup {
		m.splitSetup = false
		return ptymux.HomeAction{SplitSetup: true}
	}
	// A pending shell-out (the diff pager) — let the mux suspend the terminal for it.
	if m.suspendFn != nil {
		fn := m.suspendFn
		m.suspendFn = nil
		return ptymux.HomeAction{Suspend: fn}
	}
	if chosen != nil {
		m.markUsed(chosen.ID) // opening it = using it; keeps it atop "last used" after close
		// 'S' share: host this one session over the relay via the mux's suspend (your other
		// open sessions stay put; you return to the launcher when the share ends).
		if m.sharing {
			m.sharing = false
			return ptymux.HomeAction{Suspend: shareClosure(*chosen)}
		}
		spec := inheritRepoBindSpec(ptymux.Spec{Label: muxLabelFor(*chosen, m.meta), Key: chosen.ID, Model: sessionModel(*chosen), Argv: chosen.resumeArgv, Dir: chosen.resumeDir})
		return ptymux.HomeAction{Spawn: &spec}
	}
	if done {
		return ptymux.HomeAction{Quit: true}
	}
	return ptymux.HomeAction{} // stayed in home → mux repaints
}

// loadingFrame renders one frame of the boot splash shown while a session starts up: the brand
// wordmark with the gradient SWEEPING across it + a status line, centered. `phase` advances each
// tick. Returned to ptymux via mx.LoadingFrame. The sweep used to run on its own 28-stop rainbow
// ramp defined here; it now rides brand.WordmarkPhase, so the splash, the switchboard banner and
// the in-session overlays are one mark in one palette.
func loadingFrame(phase, cols, rows int) []byte {
	if cols < 24 || rows < 6 {
		return []byte("\x1b[2J\x1b[H  reopening…")
	}
	const word = "☎  P A R T Y L I N E"
	center := func(w int) int {
		if c := (cols - w) / 2; c > 1 {
			return c
		}
		return 1
	}
	row := rows / 2
	if row < 1 {
		row = 1
	}
	// The wordmark itself IS the animation — each glyph rides the gradient, which flows as phase
	// advances (shimmer), so no separate progress bar is needed.
	msg := "reopening your sessions…"
	var f strings.Builder
	f.WriteString("\x1b[2J")
	fmt.Fprintf(&f, "\x1b[%d;%dH%s", row, center(brand.VisWidth(word)), brand.WordmarkPhase(phase))
	fmt.Fprintf(&f, "\x1b[%d;%dH\x1b[38;5;245m%s\x1b[0m", row+2, center(len(msg)), msg)
	return []byte(f.String())
}

// runLLMSApp builds the persistent launcher (home = the llms browser) and runs the mux.
// initial specs (if any) open straight into live sessions; empty → start at the launcher.
func runLLMSApp(initial []ptymux.Spec) error {
	// tmux graduation: direct opens (`--resume`, `ptln llms <id>...`, `ptln llms new`) are
	// hosted in tmux when the backend is on — the built-in mux's tab-switch corruption needs
	// live in-process children, and this route never creates any. Bare `ptln` (no initial)
	// falls through to the launcher below, which hands opens to tmux via its Suspend flow.
	if len(initial) > 0 && useTmuxBackend() {
		return runTmuxApp(initial)
	}
	// The boot splash reports each step below as it completes. It paints NOTHING until loading
	// has run past ~150ms, so a warm launch is still instant with no flash — see llms_boot.go.
	// The sequence itself is untouched: same calls, same order.
	boot := newBootDisplay()
	all, meta, firstRun := llmsBoot(boot)
	if len(all) == 0 && len(initial) == 0 {
		boot.Done()
		return runWelcome() // nothing on the switchboard yet → the welcome screen (the empty state)
	}
	m := &aiMenu{all: all, tagline: taglines[time.Now().Minute()%len(taglines)], meta: meta, presence: &daemonPresence{}, firstRun: firstRun}
	if welcomeWantSearch { // the welcome screen's "/ find a session" door
		welcomeWantSearch, m.filter = false, true
	}
	// Recovery groundwork: capture each live session's git origin (once ever, off the UI
	// path) so a session whose dir is later deleted can be recovered. RepoChecked bounds
	// this to one git look per session across all launches — no per-launch fork storm.
	go func() {
		snapshot := append([]aiSession(nil), all...)
		scan := loadLLMMeta()
		if !captureSessionRepos(snapshot, scan) {
			return
		}
		// Merge ONLY Repo/RepoChecked into a fresh load, so a rename / markUsed the
		// main thread wrote while our git calls ran isn't clobbered by this save.
		latest := loadLLMMeta()
		for id, mt := range scan {
			if mt.Repo == "" && !mt.RepoChecked {
				continue
			}
			l := latest[id]
			if mt.Repo != "" && l.Repo == "" {
				l.Repo = mt.Repo
			}
			if mt.RepoChecked {
				l.RepoChecked = true
			}
			latest[id] = l
		}
		saveLLMMeta(latest)
	}()
	m.reloadJoinable()                    // S2: show which projects are advertised to parties
	m.setAccount(api.LoadAccount().Email) // "logged in as …" from cache — instant, no network
	m.applyFilter()
	defer m.presence.goOffline() // drop the daemon stream when the launcher exits (Offline)
	h := &llmsHome{m: m}
	// Every session the mux is about to reopen becomes its own step — the `--resume` wait scales
	// with how many there are, and it used to show nothing throughout.
	unhook := bootReportRestores(boot, initial)
	mx, err := ptymux.New(h, initial)
	unhook()
	boot.Done() // hand the screen back the moment the work is done — never held open for effect
	if err != nil {
		return err
	}
	h.mux = mx
	m.presence.wake = mx.Wake                                           // let the daemon stream surface approval banners in real time
	mx.ContextFn = func() { cgSessionMenu(mx) }                         // Common Ground: ctrl-\ c — record/view + attach a thread
	mx.MCPFn = func() { mcpSessionMenu(mx) }                            // ctrl-\ m: wire/unwire MCP servers for this session
	mx.WorktreeFn = tmuxAfterMenu(mx, func() { wtMenu(mx) })            // ctrl-\ w: fork this session into a git worktree
	mx.NewFn = tmuxAfterMenu(mx, func() { newRunMenu(mx) })             // ctrl-\ n: New/Run — fresh session · work · crank
	mx.KeepGoingFn = func() { keepGoingToggleMenu(mx) }                 // ctrl-\ g: arm/disarm keep-going on this session
	mx.ShareFn = func() { shareMenu(muxTargets{mx}) }                   // ctrl-\ s: share this session / open a shared terminal tab
	mx.PeerFn = func() { peerMenu(muxTargets{mx}) }                     // ctrl-\ p: ask a teammate's agent (ask_peer) → inject the answer
	mx.NewPaneHomeFn = func() ptymux.PaneHome { return newPaneHome(h) } // `|` / ctrl-\ |: one manager per pane
	if insidePtlnTmux() {
		startThreadCheckup(tmuxTargets{}) // Common Ground checkup over the tmux transport
	} else {
		startThreadCheckup(mx) // Common Ground: post-gap "what changed" banner when --thread is set
	}
	// ask_peer / ask_session delivery: in tmux mode the launcher fixture is the resident
	// host and the sessions are tmux panes — the same watchers run, over a tmux transport.
	if insidePtlnTmux() {
		startPeerAskAdopter(tmuxTargets{}, api.New())
		startSessionAskWatch(tmuxTargets{})
	} else {
		startPeerAskAdopter(muxTargets{mx}, api.New()) // ask_peer: watch for answers to asks the AGENT made, and deliver them
		startSessionAskWatch(muxTargets{mx})           // ask_session: carry questions between this window's own sessions
	}
	startWorkspaceWatch(mx) // keep the --resume snapshot current, so a kill can't strand it on the last clean quit
	// If the identity cache is empty (logged in before this existed), fetch it once in the
	// background and repaint — never block the launcher's startup on the network.
	if m.account() == "" && api.LoadToken() != "" {
		go func() {
			if p, err := api.New().Me(); err == nil && p.Email != "" {
				_ = api.SaveAccount(api.Account{Email: p.Email, Name: p.DisplayName})
				m.setAccount(p.Email)
				mx.Wake()
			}
		}()
	}
	mx.BeforeQuit = func(specs []ptymux.Spec) { // snapshot open sessions on quit (for `--resume`) …
		saveWorkspace(specs)
		scribeOnQuit(specs) // … and capture each thread-attached session's context before it goes cold (detached, non-blocking)
	}
	mx.Skin = themed               // re-colour the picker / quit prompt / status bar to the theme
	mx.LoadingFrame = loadingFrame // animated boot splash while a session starts (esp. --resume)
	// Resolve a live child's key (= session id) to "waiting"/"active" for the quit prompt,
	// with a fresh tail-read so the counts reflect the moment you try to quit.
	byID := make(map[string]aiSession, len(all))
	for _, s := range all {
		byID[s.ID] = s
	}
	mx.StatusFn = func(key string) string {
		if s, ok := byID[key]; ok {
			return liveStatus(s)
		}
		return ""
	}
	// Light a session's ☎ while its agent is calling a partyline tool. The resolver runs at draw
	// time; the watcher exists only to ask for a repaint, because the status row is event-driven and
	// a tool call is neither a keystroke nor a banner.
	mx.ToolActivityFn = readToolActivity
	startToolActivityWatch(muxActivity{mx})
	return mx.Run()
}

// muxActivity adapts the real Mux to the small surface the activity watch needs, converting the
// mux's LiveSession to the watcher's own key-only view so the watcher stays testable with a fake.
type muxActivity struct{ mx *ptymux.Mux }

func (m muxActivity) WakeBar() { m.mx.WakeBar() }

func (m muxActivity) LiveSessions() []LiveSessionKey {
	live := m.mx.LiveSessions()
	out := make([]LiveSessionKey, 0, len(live))
	for _, s := range live {
		out = append(out, LiveSessionKey{Key: s.Key})
	}
	return out
}
