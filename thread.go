package main

import (
	"fmt"
	"os"
	"os/exec"
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
//	ptln thread remember <id> <decision|constraint|contract|question|note> "<fact>"
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
	case "archive":
		threadArchive(c, args, true)
	case "unarchive":
		threadArchive(c, args, false)
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
	fmt.Println("ptln thread — shared context across people, machines, and engines (Common Ground)")
	fmt.Println("  ptln thread                      list your threads")
	fmt.Println("  ptln thread new \"<title>\" [--team <slug>] [--share]")
	fmt.Println("  ptln thread show <id>            the thread + its context so far")
	fmt.Println("  ptln thread share <id>           make it visible to the team (default: private)")
	fmt.Println("  ptln thread unshare <id>         make it private again")
	fmt.Println("  ptln thread remember <id> <kind> \"<fact>\" [--replaces <#>]")
	fmt.Println("                                   kinds: decision|constraint|contract|question|note")
	fmt.Println("  ptln thread archive <id>         hide it from the list (unarchive to bring back)")
	fmt.Println("  ptln thread recall <id>          everything recorded on the thread")
	fmt.Println("  ptln thread connect <engine>     wire recall/remember into codex|gemini|antigravity")
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
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln thread %s <id>", map[string]string{"team": "share", "private": "unshare"}[vis]))
	}
	if err := c.SetThreadVisibility(args[0], vis); err != nil {
		fatal(err)
	}
	if vis == "team" {
		fmt.Println("✓ shared with the team")
	} else {
		fmt.Println("✓ made private")
	}
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
		fatal(fmt.Errorf("usage: ptln thread connect <claude|codex|gemini|antigravity>"))
	}
	engine := strings.ToLower(args[0])
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "ptln"
	}
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
	var cerr error
	switch engine {
	case "claude":
		cerr = run("claude", "mcp", "add", "-s", "user", "common-ground", exe, "cg-mcp")
	case "codex":
		cerr = run("codex", "mcp", "add", "common-ground", "--", exe, "cg-mcp")
	case "gemini":
		cerr = run("gemini", "mcp", "add", "-s", "user", "--trust", "common-ground", exe, "cg-mcp")
	case "antigravity", "agy":
		// agy has no direct mcp-add; it imports MCP servers from claude's config. Register in
		// claude (user scope), then import into agy.
		if cerr = run("claude", "mcp", "add", "-s", "user", "common-ground", exe, "cg-mcp"); cerr == nil {
			cerr = run("agy", "plugin", "import", "claude")
		}
	default:
		fatal(fmt.Errorf("unknown engine %q — try: claude, codex, gemini, antigravity", engine))
	}
	if cerr != nil {
		fatal(fmt.Errorf("connect %s: %w", engine, cerr))
	}
	fmt.Printf("✓ %s connected to Common Ground. Launch attached to a thread:\n  ptln new %s --thread <id>\n", engine, engine)
}

func threadRemember(c *api.Client, args []string) {
	if len(args) < 3 {
		fatal(fmt.Errorf("usage: ptln thread remember <id> <decision|constraint|contract|question|note> \"<fact>\""))
	}
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
		fatal(fmt.Errorf("usage: ptln thread remember <id> <kind> \"<fact>\" [--supersedes <#>]"))
	}
	id, kind, body := pos[0], pos[1], strings.Join(pos[2:], " ")
	b, err := c.Remember(id, kind, body, "", "", supersedes)
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
	fmt.Println("✓ attached — this thread now inherits the project's canon")
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
