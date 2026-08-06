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
	if api.LoadToken() == "" {
		cgNote("Context Threads", []string{"you're not logged in — run `ptln login` first."})
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
			mx.SetPendingReattach(ptymux.Spec{Label: label, Key: key + "·cg", Argv: wiredArgv, Dir: dir, Thread: id, ThreadLabel: title, Engine: eng, MCPs: mcps})
			cgNote("Context Threads", []string{
				fmt.Sprintf("  %sRestarting your session%s to attach it to %s%s%s…", cgBold, cgOff, cgBold, title, cgOff),
				fmt.Sprintf("  %syour conversation carries over; give the agent a moment to reconnect.%s", cgDim, cgOff),
				"",
				"  then seed this thread from the conversation so far — run:",
				fmt.Sprintf("  %s/mcp__partyline-context-threads__seed_from_history%s", cgKey, cgOff),
				fmt.Sprintf("  %s(type / and pick partyline-context-threads)%s", cgDim, cgOff)})
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
				needThread()
				continue
			}
			cgRecord(c, target, title)
		case "v":
			if target == "" {
				needThread()
				continue
			}
			cgView(c, target)
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
				mx.SetPendingReattach(ptymux.Spec{Label: label, Key: key + "·x", Argv: unwired, Dir: dir, Engine: eng, MCPs: mcps}) // no Thread → unwired
				cgNote("Context Threads", []string{
					fmt.Sprintf("  %sRestarting your session%s to disconnect it from %s%s%s…", cgBold, cgOff, cgBold, title, cgOff),
					fmt.Sprintf("  %syour conversation carries over; the agent loses recall/remember for this thread.%s", cgDim, cgOff)})
				return
			}
			mx.SetActiveThread("", "") // shell / non-engine → just drop the record-only binding
			target, wired = "", false
			refresh()
		case "s":
			if target == "" {
				needThread()
				continue
			}
			cgShare(c, target)
			refresh() // visibility may have changed
		case "b":
			if target == "" {
				needThread()
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
				cgNote("Context Threads", []string{
					fmt.Sprintf("  %sthis session isn't inside a git repository — nothing to bind.%s", cgDim, cgOff)})
				continue
			}
			if werr := writeRepoBind(repo, target); werr != nil {
				cgNote("Context Threads", []string{fmt.Sprintf("  %s✗ %v%s", cgBad, werr, cgOff)})
				continue
			}
			cgNote("Context Threads", []string{
				fmt.Sprintf("  %s✓%s repo bound to %s%s%s", cgOK, cgOff, cgBold, title, cgOff),
				fmt.Sprintf("  %severy ptln session here auto-attaches — check .partyline.json in%s", cgDim, cgOff),
				fmt.Sprintf("  %sso the whole team's agents share it.%s", cgDim, cgOff)})
		case "w":
			if target == "" {
				needThread()
				continue
			}
			cgNote("Context Threads", []string{
				"  " + api.Base() + "/threads/" + target})
		case "t":
			if id := cgPick(c); id != "" && attach(id) {
				return
			}
		case "n":
			if id := cgNew(c); id != "" && attach(id) {
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

// cgKinds is the recordable block kinds, in the order the picker offers them. `note` is last so
// its number is stable as kinds are added — the old screen's "enter = note" default is gone with
// the cooked prompt, and enter now means what it means everywhere else in these modals: close.
var cgKinds = []string{"decision", "constraint", "contract", "question", "note"}

func cgRecord(c *api.Client, thread, title string) {
	k, _, ok := cgPicker{Title: "record to " + title,
		Body:  []string{dim("what kind of fact is this? agents weigh them differently.")},
		Items: cgKinds, Verb: "kind"}.run()
	if !ok {
		return
	}
	kind := cgKinds[k]
	body, bok := cgAsk("record to "+title, []string{dim("recording a " + kind + " as you — agents recall it")},
		sgr(cgBold, kind), "")
	if !bok || body == "" {
		cgNote("record to "+title, []string{"  " + dim("(nothing recorded)")})
		return
	}
	engine := strings.TrimSpace(os.Getenv("PARTYLINE_ENGINE"))
	b, err := c.Remember(thread, kind, body, "", engine, 0, nil)
	if err != nil {
		cgNote("record to "+title, []string{fmt.Sprintf("  %s✗ %v%s", cgBad, err, cgOff)})
		return
	}
	cgNote("record to "+title, []string{
		fmt.Sprintf("  %s✓%s %s #%d recorded", cgOK, cgOff, b.Kind, b.ID),
		"  " + dim("on the web now; agents pick it up on recall.")})
}

func cgView(c *api.Client, thread string) {
	// The context can run long, so it's a cleared scrolling screen rather than a fixed box.
	fmt.Print("\x1b[2J\x1b[H\x1b[?25h")
	fmt.Printf("\n  %s☎  context%s\n", cgBold, cgOff)
	bl, err := c.Recall(thread, 0)
	if err != nil {
		fmt.Printf("\n  %s✗ %v%s\n", cgBad, err, cgOff)
		pause(stdin())
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
	pause(stdin())
}

// cgShare lets you share the thread with a SPECIFIC team (moving it there) or make it private.
// A team of one (your personal org) shares with nobody, so we always make you pick a real team.
func cgShare(c *api.Client, thread string) {
	teams := cgTeams(c)
	lines := []string{"where should this thread live?"}
	if len(teams) == 0 {
		lines = append(lines, dim("(you're not in any teams — create one with `ptln team create`)"))
	}
	opts := []string{"private (just you)"}
	for _, t := range teams {
		opts = append(opts, "share with "+t.Name)
	}
	n, _, ok := cgPicker{Title: "Share thread", Body: lines, Items: opts, Verb: "pick one"}.run()
	if !ok {
		return
	}
	if n == 0 {
		if e := c.SetThreadVisibility(thread, "private"); e != nil {
			cgNote("Share thread", []string{fmt.Sprintf("  %s✗ %v%s", cgBad, e, cgOff)})
		} else {
			cgNote("Share thread", []string{fmt.Sprintf("  %s✓%s now private", cgOK, cgOff)})
		}
		return
	}
	if e := c.ShareThreadWithTeam(thread, teams[n-1].Slug); e != nil {
		cgNote("Share thread", []string{fmt.Sprintf("  %s✗ %v%s", cgBad, e, cgOff)})
		return
	}
	cgNote("Share thread", []string{fmt.Sprintf("  %s✓%s shared with %s", cgOK, cgOff, teams[n-1].Name)})
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

func cgPick(c *api.Client) string {
	ths, err := c.ListThreads()
	if err != nil || len(ths) == 0 {
		cgNote("Switch thread", []string{fmt.Sprintf("%s(no threads yet — n makes one)%s", cgDim, cgOff)})
		return ""
	}
	if len(ths) > 20 {
		ths = ths[:20] // the frame scrolls now, but 20 threads is already more list than a modal wants
	}
	items := make([]string, 0, len(ths))
	for _, t := range ths {
		if t.Visibility == "team" {
			items = append(items, t.Title+"  "+dim("· shared"))
			continue
		}
		items = append(items, t.Title)
	}
	n, _, ok := cgPicker{Title: "Switch thread",
		Body:  []string{dim("pick the thread to attach to this session")},
		Items: items, Verb: "attach"}.run()
	if !ok {
		return ""
	}
	return ths[n].ID
}

func cgNew(c *api.Client) string {
	t, ok := cgAsk("New thread", []string{dim("name the thread you're about to create")}, "title", "")
	if !ok {
		return ""
	}
	// Where does it live? Private (your personal space) or shared with a specific team.
	slug, vis := "", ""
	teams := cgTeams(c)
	if len(teams) > 0 {
		opts := []string{"private (just you)"}
		for _, tm := range teams {
			opts = append(opts, "share with "+tm.Name)
		}
		if n, _, pok := (cgPicker{Title: "New thread", Body: []string{dim("keep " + t + ":")},
			Items: opts, Verb: "pick one"}).run(); pok && n >= 1 {
			slug, vis = teams[n-1].Slug, "team"
		}
	}
	th, err := c.CreateThread(t, slug, vis)
	if err != nil || th == nil {
		cgNote("New thread", []string{fmt.Sprintf("  %s✗ %v%s", cgBad, err, cgOff)})
		return ""
	}
	where := "private"
	if slug != "" {
		where = "shared with the team"
	}
	cgNote("New thread", []string{
		fmt.Sprintf("  %s✓%s created %s%q%s %s", cgOK, cgOff, cgBold, th.Title, cgOff, dim("("+where+")"))})
	return th.ID // caller (attach) wires the session to it
}

func needThread() {
	cgNote("Context Threads", []string{fmt.Sprintf("%spick a thread (t) or make one (n) first.%s", cgDim, cgOff)})
}

func pause(in *bufio.Reader) {
	fmt.Printf("\n  %s(enter to continue)%s ", cgDim, cgOff)
	_, _ = in.ReadString('\n')
}
