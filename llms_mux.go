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
func startThreadCheckup(mx *ptymux.Mux) {
	thread := strings.TrimSpace(os.Getenv("PARTYLINE_THREAD_ID"))
	if thread == "" || api.LoadToken() == "" {
		return
	}
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
		for {
			time.Sleep(poll)
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

// engineBin maps a tool name (as shown in the launcher) to its executable. `agy` is
// accepted as an alias for antigravity. This is the one place new-session launching and
// the tool list agree on what to exec.
var engineBin = map[string]string{
	"claude": "claude", "codex": "codex", "gemini": "gemini",
	"antigravity": "agy", "agy": "agy",
}

// threadPrimer fetches a thread's current shared context and frames it as a system-prompt
// addition: background the agent already knows, explicitly NOT a to-do list. Best-effort —
// returns "" on any error (no thread context, not logged in, network) so launch never blocks.
func threadPrimer(threadID string) string {
	if api.LoadToken() == "" {
		return ""
	}
	_, blocks, err := api.New().GetThread(threadID)
	if err != nil || len(blocks) == 0 {
		return ""
	}
	body := formatContextBlocks(blocks) // shared with cg-mcp; hides superseded
	return "## Shared team context (partyline Common Ground)\n\n" +
		"This is background the team has recorded for this thread — decisions, constraints, " +
		"and contracts that cross the seam between people/components. Treat it as context you " +
		"already know. Do NOT act on it or change anything because of it unless the user asks. " +
		"You can pull the latest or record new shared facts with the common-ground MCP tools " +
		"(recall / remember).\n\n" + body
}

// llmsNew starts a FRESH session of an engine in the mux — the new-session counterpart to
// resuming. `ptln llms new <claude|codex|gemini|antigravity> [dir] [--thread <id>]`.
// --thread attaches the session to a Common Ground thread: the thread id rides the env
// (the mux child inherits it), so the engine's cg-mcp server reads/writes that thread.
func llmsNew(args []string) {
	var pos []string
	thread := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--thread" {
			if i++; i < len(args) {
				thread = strings.TrimSpace(args[i])
			}
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) == 0 {
		fatal(fmt.Errorf("usage: ptln llms new <claude|codex|gemini|antigravity> [dir] [--thread <id>]"))
	}
	bin := engineBin[strings.ToLower(pos[0])]
	if bin == "" {
		fatal(fmt.Errorf("unknown tool %q — try: claude, codex, gemini, antigravity", pos[0]))
	}
	if _, err := exec.LookPath(bin); err != nil {
		fatal(fmt.Errorf("%s not found on PATH — is it installed?", bin))
	}
	dir, _ := os.Getwd()
	if len(pos) > 1 && strings.TrimSpace(pos[1]) != "" {
		if abs, err := filepath.Abs(pos[1]); err == nil {
			dir = abs
		}
	}

	argv := []string{bin}
	if thread != "" {
		// The thread + engine ride the env; the mux child (ptysess) inherits os.Environ(),
		// and so does the cg-mcp server the engine spawns. No PARTYLINE_AGENT_NAME → the
		// session writes as the logged-in user (you're piloting it, not an autonomous agent).
		os.Setenv("PARTYLINE_THREAD_ID", thread)
		os.Setenv("PARTYLINE_ENGINE", strings.ToLower(pos[0]))
		switch bin {
		case "claude":
			// Register the Common Ground MCP alongside the agent's own servers (no
			// --strict-mcp-config). Token-free in argv: cg-mcp auths with the account token.
			cfg := fmt.Sprintf(`{"mcpServers":{"common-ground":{"command":%q,"args":["cg-mcp"]}}}`, selfExe())
			argv = append(argv, "--mcp-config", cfg)
			// At-launch primer (COMMON-GROUND §12.3): the thread's current shared context goes
			// into the system prompt so the agent starts already knowing it. Launch is a safe
			// delivery boundary (§5); framed as background, NOT an instruction to act. Live
			// fetch, best-effort — a slow/failed call never blocks the session.
			if primer := threadPrimer(thread); primer != "" {
				argv = append(argv, "--append-system-prompt", primer)
			}
		case "codex":
			// codex takes per-invocation MCP config via -c (TOML dotted overrides), same as the
			// party runner. No system-prompt flag → no auto-primer; the agent uses recall itself.
			exe := selfExe()
			argv = append(argv, "-c", fmt.Sprintf("mcp_servers.common-ground.command=%q", exe), "-c", `mcp_servers.common-ground.args=["cg-mcp"]`)
		default:
			// gemini / antigravity have no per-invocation MCP hook — they need a one-time
			// persistent registration. The thread still rides the env for once they're wired.
			fmt.Fprintf(os.Stderr, "note: %s needs a one-time setup — run `ptln thread connect %s` once (this session carries the thread via env).\n", bin, strings.ToLower(pos[0]))
		}
	}

	spec := ptymux.Spec{
		Label: bin,
		Key:   fmt.Sprintf("new-%s-%d", bin, time.Now().UnixNano()),
		Argv:  argv,
		Dir:   dir,
	}
	if err := runLLMSApp([]ptymux.Spec{spec}); err != nil {
		fatal(err)
	}
}

// llmsHome adapts the aiMenu (the `ptln llms` browser) to the ptymux.Home interface, so
// the mux can host it as the persistent launcher screen. The mux owns the terminal; this
// adapter just renders the menu and translates its keys into mux actions.
type llmsHome struct {
	m   *aiMenu
	mux *ptymux.Mux
}

func (h *llmsHome) Render(cols, rows int) {
	h.m.w, h.m.h = cols, rows
	if h.m.w < 40 || h.m.h < 10 {
		h.m.w, h.m.h = 80, 24
	}
	h.syncLive()
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
		specs = append(specs, ptymux.Spec{Label: muxLabelFor(s, m.meta), Key: s.ID, Model: sessionModel(s), Argv: s.resumeArgv, Dir: s.resumeDir})
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
	h.syncLive() // float live sessions to the top BEFORE the first paint (no open-time jump)
}

func (h *llmsHome) HandleKey(b []byte) ptymux.HomeAction {
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
		if len(h.mux.LiveKeys()) > 0 {
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
	// 'n' opened a plain shell as a new mux window.
	if m.spawnShell {
		m.spawnShell = false
		return ptymux.HomeAction{Spawn: shellSpec()}
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
		spec := ptymux.Spec{Label: muxLabelFor(*chosen, m.meta), Key: chosen.ID, Model: sessionModel(*chosen), Argv: chosen.resumeArgv, Dir: chosen.resumeDir}
		return ptymux.HomeAction{Spawn: &spec}
	}
	if done {
		return ptymux.HomeAction{Quit: true}
	}
	return ptymux.HomeAction{} // stayed in home → mux repaints
}

// rainbowRamp is a bright, full-spectrum 256-colour cycle (red→orange→yellow→green→cyan→blue→
// violet→magenta→back) — punchy on a dark terminal, unlike the warm lolcat ramp. Indexing it
// with (position + phase) makes the colours FLOW as phase advances.
var rainbowRamp = []int{196, 202, 208, 214, 220, 226, 190, 154, 118, 82, 46, 48, 50, 51, 45, 39, 33, 27, 21, 57, 93, 129, 165, 201, 200, 199, 198, 197}

func rainbowAt(i int) int {
	n := len(rainbowRamp)
	return rainbowRamp[((i%n)+n)%n]
}

// loadingFrame renders one frame of the boot splash shown while a session starts up: a bright
// FLOWING rainbow wordmark + a full-width solid-block rainbow bar + a status line, centered.
// `phase` advances each tick to sweep the rainbow. Returned to ptymux via mx.LoadingFrame.
func loadingFrame(phase, cols, rows int) []byte {
	if cols < 24 || rows < 6 {
		return []byte("\x1b[2J\x1b[H  reopening…")
	}
	word := "☎  P A R T Y L I N E"
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
	// The wordmark itself IS the animation — each glyph rides a bright rainbow that flows as
	// phase advances (shimmer), so no separate progress bar is needed.
	var wm strings.Builder
	wi := 0
	for _, r := range word {
		if r == ' ' {
			wm.WriteByte(' ')
			continue
		}
		fmt.Fprintf(&wm, "\x1b[1;38;5;%dm%c", rainbowAt(wi*2+phase), r)
		wi++
	}
	wm.WriteString("\x1b[0m")
	msg := "reopening your sessions…"
	var f strings.Builder
	f.WriteString("\x1b[2J")
	f.WriteString(fmt.Sprintf("\x1b[%d;%dH%s", row, center(visWidth(word)), wm.String()))
	f.WriteString(fmt.Sprintf("\x1b[%d;%dH\x1b[38;5;245m%s\x1b[0m", row+2, center(len(msg)), msg))
	return []byte(f.String())
}

// runLLMSApp builds the persistent launcher (home = the llms browser) and runs the mux.
// initial specs (if any) open straight into live sessions; empty → start at the launcher.
func runLLMSApp(initial []ptymux.Spec) error {
	loadTheme() // restore the user's chosen colour theme (persisted)
	all := collectSessions()
	if len(all) == 0 && len(initial) == 0 {
		fmt.Println("no AI CLI sessions found (looked for claude, codex, gemini, llm)")
		return nil
	}
	m := &aiMenu{all: all, tagline: taglines[time.Now().Minute()%len(taglines)], meta: loadLLMMeta(), presence: &daemonPresence{}}
	m.reloadJoinable()                    // S2: show which projects are advertised to parties
	m.setAccount(api.LoadAccount().Email) // "logged in as …" from cache — instant, no network
	m.applyFilter()
	defer m.presence.goOffline() // drop the daemon stream when the launcher exits (Offline)
	h := &llmsHome{m: m}
	mx, err := ptymux.New(h, initial)
	if err != nil {
		return err
	}
	h.mux = mx
	m.presence.wake = mx.Wake                                     // let the daemon stream surface approval banners in real time
	mx.ContextFn = cgSessionMenu                                  // Common Ground: ctrl-\ c opens the record/view shared-context menu
	mx.ShareFn = func() { mx.SetBanner(openSharedTerminalTab()) } // ctrl-\ s: shared terminal in a new tab
	startThreadCheckup(mx)                                        // Common Ground: post-gap "what changed" banner when --thread is set
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
	mx.BeforeQuit = saveWorkspace  // snapshot open sessions on quit, for `--resume`
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
	return mx.Run()
}
