package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// Common Ground (docs/COMMON-GROUND.md) — a *thread* is the home for shared context that
// crosses the seam between people/components: decisions, constraints, contracts, open
// questions. Private to you until you `share` it with your team. This is the human CLI;
// agents reach the same feed through the MCP verbs (remember/recall).
//
//	ptln thread                      list your threads
//	ptln thread new "<title>" [--team <slug>] [--share]
//	ptln thread show <id>
//	ptln thread share <id> | unshare <id>
//	ptln thread remember <id> <overview|decision|constraint|contract|question|note> "<fact>"
//	ptln thread recall <id>
func threadMain(args []string) {
	if api.LoadToken() == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login`"))
	}
	c := api.New()
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "ls", "list":
		threadList(c)
	case "new":
		threadNew(c, args)
	case "show":
		threadShow(c, args)
	case "share":
		threadSetVis(c, args, "team")
	case "unshare":
		threadSetVis(c, args, "private")
	case "remember":
		threadRemember(c, args)
	case "recall":
		threadRecall(c, args)
	case "connect":
		threadConnect(args)
	case "disconnect":
		threadDisconnect(args)
	case "archive":
		threadArchive(c, args, true)
	case "unarchive":
		threadArchive(c, args, false)
	case "bind":
		threadBind(c, args)
	case "attach":
		threadAttach(c, args)
	case "promote", "graduate": // "graduate" kept as a hidden alias (shipped in v0.1.9x)
		threadGraduate(c, args)
	case "accept":
		threadReview(c, args, "accept")
	case "reject":
		threadReview(c, args, "reject")
	case "distill":
		threadDistill(c, args)
	case "-h", "--help", "help":
		threadUsage()
	default:
		fatal(fmt.Errorf("unknown: ptln thread %q — run `ptln thread help`", sub))
	}
}

func threadUsage() {
	fmt.Println("ptln thread — shared context across people, machines, and engines (Context Threads)")
	fmt.Println("  ptln thread                      list your threads")
	fmt.Println("  ptln thread new \"<title>\" [--team <slug>] [--share]")
	fmt.Println("  ptln thread show <id>            the thread + its context so far")
	fmt.Println("  ptln thread share <id> [--team <slug>]   share with a team (picks/moves it there)")
	fmt.Println("  ptln thread unshare <id>         make it private again")
	fmt.Println("  ptln thread remember <id> <kind> \"<fact>\" [--replaces <#>]")
	fmt.Println("                                   kinds: overview|decision|constraint|contract|question|note")
	fmt.Println("  ptln thread archive <id>         hide it from the list (unarchive to bring back)")
	fmt.Println("  ptln thread recall <id>          everything recorded on the thread")
	fmt.Println("  ptln thread bind [<id>|--clear]  bind THIS REPO to a thread (.partyline.json — check it")
	fmt.Println("                                   in, and every ptln session here auto-attaches)")
	fmt.Println("  ptln thread connect <engine>     wire recall/remember into claude|codex|gemini|antigravity")
	fmt.Println("  ptln thread connect --all        wire every AI CLI installed on this machine")
	fmt.Println("  ptln thread disconnect <engine>  remove that wiring again")
	fmt.Println("  ptln thread attach <id> <project>            attach the thread to a project")
	fmt.Println("  ptln thread promote <id> <#> <project>       promote a fact into the project")
	fmt.Println("  ptln thread accept/reject <id> <#>           confirm/drop a [proposed] scribe fact")
	fmt.Println("  ptln thread distill <party-id>               run the scribe over a party's chat")
}

func threadList(c *api.Client) {
	ts, err := c.ListThreads()
	if err != nil {
		fatal(err)
	}
	if len(ts) == 0 {
		fmt.Println("no threads yet — `ptln thread new \"<title>\"`")
		return
	}
	fmt.Printf("%-38s  %-7s  %s\n", "ID", "VIS", "TITLE")
	for _, t := range ts {
		vis := "private"
		if t.Visibility == "team" {
			vis = "shared"
		}
		fmt.Printf("%-38s  %-7s  %s\n", t.ID, vis, t.Title)
	}
}

func threadNew(c *api.Client, args []string) {
	title, team, share := "", "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--team":
			if i++; i < len(args) {
				team = args[i]
			}
		case "--share":
			share = true
		default:
			if title == "" {
				title = args[i]
			}
		}
	}
	if strings.TrimSpace(title) == "" {
		fatal(fmt.Errorf("usage: ptln thread new \"<title>\" [--team <slug>] [--share]"))
	}
	vis := ""
	if share {
		vis = "team"
	}
	t, err := c.CreateThread(title, team, vis)
	if err != nil {
		fatal(err)
	}
	where := "private to you"
	if t.Visibility == "team" {
		where = "shared with the team"
	}
	fmt.Printf("✓ thread %s — %q (%s)\n", t.ID, t.Title, where)
	fmt.Printf("  attach a session:  ptln new <tool> --thread %s\n", t.ID)
	fmt.Printf("  record a fact:     ptln thread remember %s decision \"…\"\n", t.ID)
}

func threadSetVis(c *api.Client, args []string, vis string) {
	var id, team string
	for i := 0; i < len(args); i++ {
		if args[i] == "--team" {
			if i++; i < len(args) {
				team = args[i]
			}
			continue
		}
		if id == "" && !strings.HasPrefix(args[i], "-") {
			id = args[i]
		}
	}
	if id == "" {
		fatal(fmt.Errorf("usage: ptln thread %s <id> [--team <slug>]", map[string]string{"team": "share", "private": "unshare"}[vis]))
	}
	if vis == "private" {
		if err := c.SetThreadVisibility(id, "private"); err != nil {
			fatal(err)
		}
		fmt.Println("✓ made private")
		return
	}
	// share: with a named team (moves the thread there), or — if none — within the thread's
	// current org, which is a footgun for personal threads (a team of one shares with nobody).
	if team != "" {
		if err := c.ShareThreadWithTeam(id, team); err != nil {
			fatal(err)
		}
		fmt.Printf("✓ shared with team %s\n", team)
		return
	}
	if err := c.SetThreadVisibility(id, "team"); err != nil {
		fatal(err)
	}
	fmt.Println("✓ shared within this thread's team")
	fmt.Printf("  %s\n", dim("tip: `ptln thread share <id> --team <slug>` to share with a specific team"))
	fmt.Printf("  %s\n", dim("(a personal thread shared this way is still visible only to you — pick a team)"))
}

func threadShow(c *api.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln thread show <id>"))
	}
	t, blocks, err := c.GetThread(args[0])
	if err != nil {
		fatal(err)
	}
	vis := "private"
	if t.Visibility == "team" {
		vis = "shared"
	}
	fmt.Printf("%s  (%s)\n", t.Title, vis)
	printBlocks(blocks)
}

func threadRecall(c *api.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln thread recall <id>"))
	}
	blocks, err := c.Recall(args[0], 0)
	if err != nil {
		fatal(err)
	}
	printBlocks(blocks)
}

func printBlocks(blocks []api.ContextBlock) {
	if len(blocks) == 0 {
		fmt.Println("  (nothing recorded yet)")
		return
	}
	for _, b := range blocks {
		tag := ""
		if b.Status != "open" {
			tag = " [" + b.Status + "]"
		}
		fmt.Printf("  • #%-3d %-10s %s%s\n", b.ID, b.Kind, b.Body, tag)
		fmt.Printf("        %s · %s\n", b.Author, humanAge(parseTime(b.CreatedAt)))
	}
}

// threadConnect wires the Common Ground MCP (recall/remember) into an engine so agents in
// non-claude sessions can use it. claude + codex are per-invocation via `ptln new --thread`
// (no setup needed) — but connect makes it persistent for sessions launched outside the mux;
// gemini + antigravity have no per-invocation hook, so they REQUIRE this one-time setup. We
// use each engine's OWN `mcp add` / `plugin import` (never hand-edit their config). The active
// thread is targeted per-launch via PARTYLINE_THREAD_ID (set by `ptln new … --thread`); with
// no thread the tools no-op gracefully.
func threadConnect(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln thread connect <claude|codex|gemini|antigravity|--all>"))
	}
	// #557 — `--all` wires every engine actually installed on this machine, which is what the
	// first-run prompt does and what someone re-running setup on a new laptop wants. Non-interactive
	// by design: scriptable, and the honest answer for a machine with no engines is to say so.
	if args[0] == "--all" || args[0] == "all" {
		installed := installedEngines()
		if len(installed) == 0 {
			fmt.Println("No supported AI CLIs found on PATH (claude, codex, gemini, agy).")
			return
		}
		st := loadMCPOffer()
		for _, e := range installed {
			if err := connectEngineQuiet(e.key); err != nil {
				fmt.Printf("✗ %s: %v\n", e.name, err)
				continue
			}
			st.Answered[e.key] = "connected"
			fmt.Printf("✓ %s\n", e.name)
		}
		saveMCPOffer(st)
		return
	}
	engine := strings.ToLower(args[0])
	if err := connectEngineQuiet(engine); err != nil {
		fatal(err)
	}
	// Remember it, so the first-run prompt never offers an engine the user already wired by hand.
	st := loadMCPOffer()
	st.Answered[engine] = "connected"
	saveMCPOffer(st)
	fmt.Printf("✓ %s connected to partyline context threads. Launch attached to a thread:\n  ptln new %s --thread <id>\n", engine, engine)
}

// connectEngineQuiet registers the context-threads MCP server with one engine and RETURNS an error
// instead of exiting. Extracted from threadConnect because the first-run prompt wires several
// engines in a row (#557): one failing engine must not take the others down with it, and `fatal`
// mid-loop would do exactly that.
func connectEngineQuiet(engine string) error {
	exe, err := os.Executable()
	// Registering writes an ABSOLUTE PATH into the engine's config, where it stays until someone
	// re-registers. If that path is a temp build, the user's MCP server silently dies the moment the
	// file is cleaned up — and the symptom (recall/remember just stopped existing) points nowhere
	// near the cause.
	//
	// Found by doing exactly this: a `go build -o /tmp/…` smoke test wrote /tmp into three real
	// engine configs. Refusing is right — someone testing a dev build wants to know, and someone on
	// an installed binary never sees this.
	if err == nil && isEphemeralPath(exe) {
		return fmt.Errorf("refusing to register a temporary binary (%s) — engine configs store an absolute "+
			"path, so this would break as soon as the file is removed. Run the installed `ptln`, or set "+
			"PARTYLINE_ALLOW_TEMP_CONNECT=1 if you really mean it", exe)
	}
	if err != nil || exe == "" {
		exe = "ptln"
	}
	run := func(name string, a ...string) error {
		out, e := exec.Command(name, a...).CombinedOutput()
		s := strings.TrimSpace(string(out))
		if s != "" {
			fmt.Println("  " + strings.ReplaceAll(s, "\n", "\n  "))
		}
		if _, le := exec.LookPath(name); le != nil {
			return fmt.Errorf("%s not found on PATH", name)
		}
		// ALREADY REGISTERED IS SUCCESS. Every engine CLI here exits non-zero when the server is
		// already in its config, which is the desired end state, not a failure.
		//
		// This mattered far more than it looks. Antigravity's path is `claude mcp add` THEN
		// `agy plugin import` — so the moment claude was wired, the add "failed", the import never
		// ran, and antigravity could never succeed again for anyone. Combined with the offer state
		// only recording successes, that turned into a prompt on every single ptln invocation.
		if e != nil && alreadyRegistered(s) {
			return nil
		}
		return e
	}
	var cerr error
	switch engine {
	case "claude":
		cerr = run("claude", "mcp", "add", "-s", "user", "partyline-context-threads", exe, "cg-mcp")
	case "codex":
		cerr = run("codex", "mcp", "add", "partyline-context-threads", "--", exe, "cg-mcp")
	case "gemini":
		cerr = run("gemini", "mcp", "add", "-s", "user", "--trust", "partyline-context-threads", exe, "cg-mcp")
	case "antigravity", "agy":
		// agy has no direct mcp-add; it imports MCP servers from claude's config. Register in
		// claude (user scope), then import into agy.
		// The import runs whether or not the claude entry was added just now — "already there" is
		// exactly the state the import needs, so treating it as a reason to stop was backwards.
		if cerr = run("claude", "mcp", "add", "-s", "user", "partyline-context-threads", exe, "cg-mcp"); cerr == nil {
			cerr = run("agy", "plugin", "import", "claude")
		}
	default:
		return fmt.Errorf("unknown engine %q — try: claude, codex, gemini, antigravity", engine)
	}
	if cerr != nil {
		return fmt.Errorf("connect %s: %w", engine, cerr)
	}
	return nil
}

// threadDisconnect is the inverse of threadConnect: it removes the persistent partyline-context-threads MCP
// registration from the engine's config, so that engine no longer has recall/remember at all.
func threadDisconnect(args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln thread disconnect <claude|codex|gemini|antigravity>"))
	}
	engine := strings.ToLower(args[0])
	run := func(name string, a ...string) error {
		out, e := exec.Command(name, a...).CombinedOutput()
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Println("  " + strings.ReplaceAll(s, "\n", "\n  "))
		}
		if _, le := exec.LookPath(name); le != nil {
			return fmt.Errorf("%s not found on PATH", name)
		}
		return e
	}
	// Remove the current name AND the legacy "common-ground" (pre-rename), so upgraders don't keep
	// a dead duplicate registration. The legacy removal is best-effort — ignore its error.
	var cerr error
	switch engine {
	case "claude":
		cerr = run("claude", "mcp", "remove", "-s", "user", "partyline-context-threads")
		_ = run("claude", "mcp", "remove", "-s", "user", "common-ground")
	case "codex":
		cerr = run("codex", "mcp", "remove", "partyline-context-threads")
		_ = run("codex", "mcp", "remove", "common-ground")
	case "gemini":
		cerr = run("gemini", "mcp", "remove", "partyline-context-threads")
		_ = run("gemini", "mcp", "remove", "common-ground")
	case "antigravity", "agy":
		cerr = run("claude", "mcp", "remove", "-s", "user", "partyline-context-threads")
		_ = run("claude", "mcp", "remove", "-s", "user", "common-ground")
		if cerr == nil {
			cerr = run("agy", "plugin", "import", "claude") // re-sync agy after removing from claude
		}
	default:
		fatal(fmt.Errorf("unknown engine %q — try: claude, codex, gemini, antigravity", engine))
	}
	if cerr != nil {
		fatal(fmt.Errorf("disconnect %s: %w", engine, cerr))
	}
	fmt.Printf("✓ %s disconnected from partyline context threads (recall/remember removed).\n", engine)
}

func threadRemember(c *api.Client, args []string) {
	if len(args) < 3 {
		fatal(fmt.Errorf("usage: ptln thread remember <id> <overview|decision|constraint|contract|question|note> \"<fact>\""))
	}
	var pos []string
	var supersedes int64
	var entities []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--supersedes" || args[i] == "--supersede" || args[i] == "--replaces" || args[i] == "--replace" {
			if i++; i < len(args) {
				supersedes, _ = strconv.ParseInt(strings.TrimPrefix(args[i], "#"), 10, 64)
			}
			continue
		}
		if args[i] == "--entities" || args[i] == "--entity" {
			if i++; i < len(args) {
				for _, e := range strings.Split(args[i], ",") {
					if e = strings.TrimSpace(e); e != "" {
						entities = append(entities, e)
					}
				}
			}
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) < 3 {
		fatal(fmt.Errorf("usage: ptln thread remember <id> <kind> \"<fact>\" [--supersedes <#>] [--entities a,b]"))
	}
	id, kind, body := pos[0], pos[1], strings.Join(pos[2:], " ")
	b, err := c.Remember(id, kind, body, "", "", supersedes, entities)
	if err != nil {
		fatal(err)
	}
	if supersedes > 0 {
		fmt.Printf("✓ remembered (%s, #%d) — replaces #%d\n", b.Kind, b.ID, supersedes)
	} else {
		fmt.Printf("✓ remembered (%s, #%d)\n", b.Kind, b.ID)
	}
}

// threadReview accepts (→ visible to agents) or rejects (→ deleted) a scribe proposal (a
// [proposed] block from `ptln thread show`).
func threadReview(c *api.Client, args []string, action string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: ptln thread %s <thread-id> <#>", action))
	}
	bid, err := strconv.ParseInt(strings.TrimPrefix(args[1], "#"), 10, 64)
	if err != nil {
		fatal(fmt.Errorf("block # must be a number (see `ptln thread show <id>`)"))
	}
	if err := c.ReviewBlock(args[0], bid, action); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ %sed #%d\n", action, bid)
}

// threadDistill runs the ambient scribe over a party's chat, proposing seam facts into its
// linked thread (pending review). On-demand trigger for the server-side distiller.
func threadDistill(c *api.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln thread distill <party-id>"))
	}
	n, err := c.DistillParty(args[0])
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ distilled — %d fact(s) proposed (review with `ptln thread show <thread>` → accept/reject)\n", n)
}

func threadAttach(c *api.Client, args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: ptln thread attach <thread-id> <project-id>"))
	}
	if err := c.AttachThreadProject(args[0], args[1]); err != nil {
		fatal(err)
	}
	fmt.Println("✓ attached — this thread now inherits the project's facts")
}

// threadGraduate promotes a thread fact (#) into a project's canon (owner-gated server-side).
//
//	ptln thread graduate <thread-id> <#> <project-id> [--supersedes <#>]
func threadGraduate(c *api.Client, args []string) {
	var pos []string
	var supersedes int64
	for i := 0; i < len(args); i++ {
		if args[i] == "--supersedes" || args[i] == "--supersede" || args[i] == "--replaces" || args[i] == "--replace" {
			if i++; i < len(args) {
				supersedes, _ = strconv.ParseInt(strings.TrimPrefix(args[i], "#"), 10, 64)
			}
			continue
		}
		pos = append(pos, args[i])
	}
	if len(pos) < 3 {
		fatal(fmt.Errorf("usage: ptln thread promote <thread-id> <#> <project-id> [--replaces <#>]"))
	}
	blockID, err := strconv.ParseInt(strings.TrimPrefix(pos[1], "#"), 10, 64)
	if err != nil {
		fatal(fmt.Errorf("block # must be a number (see `ptln thread show <id>`)"))
	}
	b, err := c.GraduateBlock(pos[0], blockID, pos[2], supersedes)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ promoted to the project (%s, #%d) — the thread block is now marked promoted\n", b.Kind, b.ID)
}

func threadArchive(c *api.Client, args []string, archived bool) {
	if len(args) < 1 {
		verb := map[bool]string{true: "archive", false: "unarchive"}[archived]
		fatal(fmt.Errorf("usage: ptln thread %s <id>", verb))
	}
	if err := c.SetThreadArchived(args[0], archived); err != nil {
		fatal(err)
	}
	if archived {
		fmt.Println("✓ archived (hidden from `ptln thread ls`)")
	} else {
		fmt.Println("✓ unarchived")
	}
}

// isEphemeralPath reports whether a binary lives somewhere that will not survive — a temp dir or a
// `go run` build cache. Deliberately conservative: only paths that are unambiguously throwaway, so
// an unusual-but-real install location is never refused.
func isEphemeralPath(p string) bool {
	if os.Getenv("PARTYLINE_ALLOW_TEMP_CONNECT") != "" {
		return false
	}
	// Resolve symlinks ONLY if that succeeds. EvalSymlinks returns "" on a path that does not exist,
	// and blindly taking that turns every check below into a no-op — the guard would silently pass
	// everything, which is the one behaviour it must never have.
	if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved != "" {
		p = resolved
	}
	for _, pre := range []string{"/tmp/", "/private/tmp/", "/var/folders/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return strings.Contains(p, "/go-build")
}

// alreadyRegistered recognises an engine CLI saying "this server is already in my config" — which
// is SUCCESS for our purposes (the end state is what we wanted) even though every one of them
// signals it with a non-zero exit.
//
// Matched on the message rather than the exit code because the code is uniformly 1 and carries no
// other information. Deliberately narrow: a phrase that only appears in this specific case, so a
// real failure is never swallowed.
func alreadyRegistered(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "already exists") ||
		strings.Contains(l, "already registered") ||
		strings.Contains(l, "already configured")
}
