package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
	"partyline.sh/partyline/internal/ptymux"
)

// The `ctrl-\ c` context menu is cooked-mode prompt UI (the mux restores a normal
// terminal before calling us; it repaints the live session after we return).
// One tiny palette + one row helper so every screen of this menu reads the same.
const (
	cgBold = "\x1b[1m"
	cgDim  = "\x1b[38;5;245m"
	cgKey  = "\x1b[36m"      // action keys
	cgWire = "\x1b[38;5;39m" // wired (matches the mux's ☎ tab marker)
	cgOK   = "\x1b[32m"
	cgBad  = "\x1b[31m"
	cgOff  = "\x1b[0m"
)

// cgItem prints one aligned action row: colored key, label, optional dim note.
func cgItem(key, label, note string) {
	if note == "" {
		fmt.Printf("    %s%s%s  %s\n", cgKey, key, cgOff, label)
		return
	}
	pad := 26 - len(label)
	if pad < 2 {
		pad = 2
	}
	fmt.Printf("    %s%s%s  %s%s%s%s%s\n", cgKey, key, cgOff, label, strings.Repeat(" ", pad), cgDim, note, cgOff)
}

// cgSessionMenu is the `ctrl-\ c` overlay (COMMON-GROUND §12): the most common shared-context
// actions — record a fact, view the context, pick/create a thread, share, open the web link —
// without leaving the mux or memorising CLI verbs.
//
// Recording here writes as YOU (the logged-in user), straight to the API — no agent involved.
// That's why it works in any session, even one whose agent wasn't launched with the recall/
// remember tools. Giving the *running* agent those tools still needs a relaunch with --thread.
func cgSessionMenu(mx *ptymux.Mux) {
	in := stdin()
	if api.LoadToken() == "" {
		cgBox("Context Threads", []string{"you're not logged in — run `ptln login` first."})
		pause(in)
		return
	}
	c := api.New()
	// The thread bound to this session (mux-tracked; launch env as fallback), and whether the
	// AGENT is actually wired to it (has recall/remember) vs. record-only.
	target, wired := mx.ActiveThreadInfo()
	if target == "" {
		target = strings.TrimSpace(os.Getenv("PARTYLINE_THREAD_ID"))
		wired = target != "" // a launch env means it was launched with --thread (wired)
	}

	// Thread title/visibility are fetched once per STATE CHANGE, not per redraw — the menu
	// must not lag a network round-trip on every keypress.
	title, shared := "", false
	refresh := func() {
		title, shared = target, false
		if target == "" {
			return
		}
		if th, _, err := c.GetThread(target); err == nil && th != nil {
			title, shared = th.Title, th.Visibility == "team"
		}
	}
	refresh()

	// attach binds `id` to this session. For a real AI engine we RELAUNCH it wired (queued — the
	// mux does the restart after this menu closes) so the agent truly gets the tools. For anything
	// else (a plain shell) we bind record-only. Returns true when a relaunch was queued.
	attach := func(id string) bool {
		target = id
		refresh()
		argv, dir, label, key, ok := mx.ActiveLaunch()
		if ok && len(argv) > 0 && isWireableEngine(argv[0]) {
			mcps := mx.ActiveMCPs() // keep this session's ctrl-\ m servers through the relaunch
			wiredArgv, eng := wireSessionArgv(argv[0], carryConversation(argv[0], argv), id, mcps)
			fmt.Printf("\n  %sRestarting your session%s to attach it to %s%s%s…\n", cgBold, cgOff, cgBold, title, cgOff)
			fmt.Printf("  %syour conversation carries over; give the agent a moment to reconnect.%s\n", cgDim, cgOff)
			fmt.Println("\n  then seed this thread from the conversation so far — run:")
			fmt.Printf("  %s/mcp__partyline-context-threads__seed_from_history%s  %s(type / and pick partyline-context-threads)%s\n", cgKey, cgOff, cgDim, cgOff)
			mx.SetPendingReattach(ptymux.Spec{Label: label, Key: key + "·cg", Argv: wiredArgv, Dir: dir, Thread: id, ThreadLabel: title, Engine: eng, MCPs: mcps})
			pause(in)
			return true
		}
		mx.SetActiveThread(id, title) // shell / non-engine → record-only (the menu writes as you)
		wired = false
		return false
	}

	for {
		var lines []string
		if target != "" {
			vis := "private"
			if shared {
				vis = "shared with team"
			}
			if wired {
				lines = append(lines, fmt.Sprintf("%s●%s %s%s%s  %s%s · agent wired%s", cgWire, cgOff, cgBold, title, cgOff, cgDim, vis, cgOff))
			} else {
				lines = append(lines, fmt.Sprintf("%s○%s %s%s%s  %s%s · recording only%s", cgDim, cgOff, cgBold, title, cgOff, cgDim, vis, cgOff))
			}
			lines = append(lines, "",
				cgRow("r", "record a fact", "writes as you; agents recall it"),
				cgRow("v", "view context", "the live facts agents see"))
			if !wired {
				lines = append(lines, cgRow("a", "wire the agent", "restart with recall/remember"))
			}
			lines = append(lines, "",
				cgRow("t", "switch thread", ""),
				cgRow("n", "new thread", ""),
				cgRow("s", "share / make private", ""),
				cgRow("b", "bind repo to this thread", "team default via .partyline.json"),
				cgRow("w", "web link", ""),
				"",
				cgRow("d", "disconnect", "unwire + stop recording here"))
		} else {
			lines = append(lines,
				fmt.Sprintf("%s○%s %sno thread attached%s  %sshared context is off for this session%s", cgDim, cgOff, cgBold, cgOff, cgDim, cgOff),
				"",
				cgRow("t", "attach a thread", ""),
				cgRow("n", "new thread", ""))
		}
		lines = append(lines, "", cgHintRow("CONTEXT"))
		cgBox("Context Threads", lines)

		switch string(menuKey()) { // single keypress (already lowercased)
		case "q", "\n", "\x00": // q / enter / esc → close
			return
		case "r":
			if target == "" {
				needThread(in)
				continue
			}
			cgRecord(c, in, target, title)
		case "v":
			if target == "" {
				needThread(in)
				continue
			}
			cgView(c, in, target)
		case "a":
			if target != "" && !wired && attach(target) {
				return
			}
		case "d":
			if target == "" {
				continue
			}
			argv, dir, label, key, ok := mx.ActiveLaunch()
			if ok && len(argv) > 0 && isWireableEngine(argv[0]) {
				mcps := mx.ActiveMCPs() // drop the thread, keep the session's ctrl-\ m servers
				unwired, eng := wireSessionArgv(argv[0], argv, "", mcps)
				fmt.Printf("\n  %sRestarting your session%s to disconnect it from %s%s%s…\n", cgBold, cgOff, cgBold, title, cgOff)
				fmt.Printf("  %syour conversation carries over; the agent loses recall/remember for this thread.%s\n", cgDim, cgOff)
				mx.SetPendingReattach(ptymux.Spec{Label: label, Key: key + "·x", Argv: unwired, Dir: dir, Engine: eng, MCPs: mcps}) // no Thread → unwired
				pause(in)
				return
			}
			mx.SetActiveThread("", "") // shell / non-engine → just drop the record-only binding
			target, wired = "", false
			refresh()
		case "s":
			if target == "" {
				needThread(in)
				continue
			}
			cgShare(c, in, target)
			refresh() // visibility may have changed
		case "b":
			if target == "" {
				needThread(in)
				continue
			}
			// Bind the session's repo to this thread: .partyline.json at the repo root —
			// checked in, every teammate's ptln sessions here auto-attach (--no-thread skips).
			_, bdir, _, _, bok := mx.ActiveLaunch()
			if !bok || bdir == "" {
				bdir, _ = os.Getwd()
			}
			repo, rerr := gitwt.RepoRoot(bdir)
			if rerr != nil {
				fmt.Printf("\n  %sthis session isn't inside a git repository — nothing to bind.%s\n", cgDim, cgOff)
				pause(in)
				continue
			}
			if werr := writeRepoBind(repo, target); werr != nil {
				fmt.Printf("\n  %s✗ %v%s\n", cgBad, werr, cgOff)
			} else {
				fmt.Printf("\n  %s✓%s repo bound to %s%s%s — every ptln session here auto-attaches.\n", cgOK, cgOff, cgBold, title, cgOff)
				fmt.Printf("  %scheck .partyline.json in so the whole team's agents share it.%s\n", cgDim, cgOff)
			}
			pause(in)
		case "w":
			if target == "" {
				needThread(in)
				continue
			}
			fmt.Printf("\n  %s/threads/%s\n", api.Base(), target)
			pause(in)
		case "t":
			if id := cgPick(c, in); id != "" && attach(id) {
				return
			}
		case "n":
			if id := cgNew(c, in); id != "" && attach(id) {
				return
			}
		}
	}
}

// cgFrame clears the screen and draws the modal's framed header.
func cgFrame(title string) {
	fmt.Print("\x1b[2J\x1b[H")
	fmt.Printf("\n  %s☎  %s%s\n", cgBold, title, cgOff)
	fmt.Println("  \x1b[38;5;240m────────────────────────────────────────────\x1b[0m")
}

// cgKindTag colors a block kind for the view (subtle — the fact is the content, not the tag).
func cgKindTag(kind string) string {
	color := cgDim
	switch kind {
	case "decision":
		color = "\x1b[38;5;39m"
	case "constraint":
		color = "\x1b[33m"
	case "contract":
		color = "\x1b[35m"
	case "question":
		color = "\x1b[36m"
	}
	return fmt.Sprintf("%s[%s]%s", color, kind, cgOff)
}

func cgRecord(c *api.Client, in *bufio.Reader, thread, title string) {
	cgBox("record to "+title, []string{
		fmt.Sprintf("kind:  %s1%s decision   %s2%s constraint   %s3%s contract", cgKey, cgOff, cgKey, cgOff, cgKey, cgOff),
		fmt.Sprintf("       %s4%s question   %s5%s note   %s(enter = note)%s", cgKey, cgOff, cgKey, cgOff, cgDim, cgOff),
	})
	sel, ok := Input("kind", "note")
	if !ok {
		return
	}
	kind := "note"
	switch strings.ToLower(sel) {
	case "1", "decision", "d":
		kind = "decision"
	case "2", "constraint", "c":
		kind = "constraint"
	case "3", "contract":
		kind = "contract"
	case "4", "question":
		kind = "question"
	default:
		kind = "note"
	}
	body, bok := Input(sgr(cgBold, kind), "")
	if !bok || body == "" {
		fmt.Printf("  %s\n", dim("(nothing recorded)"))
		pause(in)
		return
	}
	engine := strings.TrimSpace(os.Getenv("PARTYLINE_ENGINE"))
	b, err := c.Remember(thread, kind, body, "", engine, 0, nil)
	if err != nil {
		fmt.Printf("  %s✗ %v%s\n", cgBad, err, cgOff)
		pause(in)
		return
	}
	fmt.Printf("  %s✓%s %s #%d recorded — on the web now; agents pick it up on recall.\n", cgOK, cgOff, b.Kind, b.ID)
	pause(in)
}

func cgView(c *api.Client, in *bufio.Reader, thread string) {
	// The context can run long, so it's a cleared scrolling screen rather than a fixed box.
	fmt.Print("\x1b[2J\x1b[H\x1b[?25h")
	fmt.Printf("\n  %s☎  context%s\n", cgBold, cgOff)
	bl, err := c.Recall(thread, 0)
	if err != nil {
		fmt.Printf("\n  %s✗ %v%s\n", cgBad, err, cgOff)
		pause(in)
		return
	}
	fmt.Println()
	// Same live-only filter agents see (§ safety): hide replaced/pruned/proposed.
	live := func(b api.ContextBlock) bool {
		return b.Status != "superseded" && b.Status != "pruned" && b.Status != "proposed"
	}
	n := 0
	// Overview leads — the same order agents get it (orientation before details).
	for _, b := range bl {
		if !live(b) || b.Kind != "overview" {
			continue
		}
		n++
		fmt.Printf("  %swhat this is about%s\n  %s\n\n", cgDim, cgOff, b.Body)
	}
	for _, b := range bl {
		if !live(b) || b.Kind == "overview" {
			continue
		}
		n++
		tag := ""
		if b.Status == "graduated" {
			tag = " \x1b[38;5;39m↳promoted\x1b[0m"
		}
		fmt.Printf("  %s#%d%s %s%s %s\n      %s— %s%s\n", cgDim, b.ID, cgOff, cgKindTag(b.Kind), tag, b.Body, cgDim, b.Author, cgOff)
	}
	if n == 0 {
		fmt.Printf("  %s(nothing recorded yet — r records the first fact)%s\n", cgDim, cgOff)
	}
	pause(in)
}

// cgShare lets you share the thread with a SPECIFIC team (moving it there) or make it private.
// A team of one (your personal org) shares with nobody, so we always make you pick a real team.
func cgShare(c *api.Client, in *bufio.Reader, thread string) {
	teams := cgTeams(c)
	lines := []string{"where should this thread live?"}
	if len(teams) == 0 {
		lines = append(lines, dim("(you're not in any teams — create one with `ptln team create`)"))
	}
	cgBox("Share thread", lines)
	opts := []string{"private (just you)"}
	for _, t := range teams {
		opts = append(opts, "share with "+t.Name)
	}
	n, ok := Pick("number", opts, func(s string) string { return s })
	if !ok {
		return
	}
	if n == 0 {
		if e := c.SetThreadVisibility(thread, "private"); e != nil {
			fmt.Printf("  %s✗ %v%s\n", cgBad, e, cgOff)
		} else {
			fmt.Printf("  %s✓%s now private\n", cgOK, cgOff)
		}
		pause(in)
		return
	}
	if e := c.ShareThreadWithTeam(thread, teams[n-1].Slug); e != nil {
		fmt.Printf("  %s✗ %v%s\n", cgBad, e, cgOff)
	} else {
		fmt.Printf("  %s✓%s shared with %s\n", cgOK, cgOff, teams[n-1].Name)
	}
	pause(in)
}

// cgTeams returns the caller's teams (non-personal orgs) for the share/create pickers.
func cgTeams(c *api.Client) []api.Org {
	orgs, err := c.ListOrgs()
	if err != nil {
		return nil
	}
	var teams []api.Org
	for _, o := range orgs {
		if !o.Personal {
			teams = append(teams, o)
		}
	}
	return teams
}

func cgPick(c *api.Client, in *bufio.Reader) string {
	ths, err := c.ListThreads()
	if err != nil || len(ths) == 0 {
		cgBox("Switch thread", []string{fmt.Sprintf("%s(no threads yet — n makes one)%s", cgDim, cgOff)})
		pause(in)
		return ""
	}
	if len(ths) > 20 {
		ths = ths[:20]
	}
	cgBox("Switch thread", []string{dim("pick the thread to attach to this session")})
	n, ok := Pick("number", ths, func(t api.Thread) string {
		if t.Visibility == "team" {
			return t.Title + "  " + dim("· shared")
		}
		return t.Title
	})
	if !ok {
		return ""
	}
	return ths[n].ID
}

func cgNew(c *api.Client, in *bufio.Reader) string {
	cgBox("New thread", []string{"name the thread you're about to create"})
	t, ok := Input("title", "")
	if !ok {
		return ""
	}
	// Where does it live? Private (your personal space) or shared with a specific team.
	slug, vis := "", ""
	teams := cgTeams(c)
	if len(teams) > 0 {
		cgBox("New thread", []string{"keep it:"})
		opts := []string{"private (just you)"}
		for _, tm := range teams {
			opts = append(opts, "share with "+tm.Name)
		}
		if n, pok := Pick("number", opts, func(s string) string { return s }); pok && n >= 1 {
			slug, vis = teams[n-1].Slug, "team"
		}
	}
	th, err := c.CreateThread(t, slug, vis)
	if err != nil || th == nil {
		fmt.Printf("  %s✗ %v%s\n", cgBad, err, cgOff)
		pause(in)
		return ""
	}
	where := "private"
	if slug != "" {
		where = "shared with the team"
	}
	fmt.Printf("  %s✓%s created %s%q%s (%s)\n", cgOK, cgOff, cgBold, th.Title, cgOff, where)
	return th.ID // caller (attach) wires the session to it
}

func needThread(in *bufio.Reader) {
	cgBox("Context Threads", []string{fmt.Sprintf("%spick a thread (t) or make one (n) first.%s", cgDim, cgOff)})
	pause(in)
}

func pause(in *bufio.Reader) {
	fmt.Printf("\n  %s(enter to continue)%s ", cgDim, cgOff)
	_, _ = in.ReadString('\n')
}
