package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/ptymux"
)

// cgSessionMenu is the `ctrl-\ c` overlay (COMMON-GROUND §12): a small interactive prompt for the
// most common shared-context actions — record a fact, view the context, pick/create a thread,
// share, open the web link — without leaving the mux or memorising CLI verbs. The mux has already
// restored a normal (cooked) terminal for us; we just prompt, act, and return — it repaints the
// live session afterward.
//
// Recording here writes as YOU (the logged-in user), straight to the API — no agent involved.
// That's why it works in any session, even one whose agent wasn't launched with the recall/
// remember tools. Giving the *running* agent those tools still needs a relaunch with --thread.
func cgSessionMenu(mx *ptymux.Mux) {
	in := bufio.NewReader(os.Stdin)
	if api.LoadToken() == "" {
		cgFrame("Context Threads")
		fmt.Println("  You're not logged in — run `ptln login` first.")
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
	titleOf := func(id string) string {
		if th, _, err := c.GetThread(id); err == nil && th != nil {
			return th.Title
		}
		return id
	}
	// attach binds `id` to this session. For a real AI engine we RELAUNCH it wired (queued — the
	// mux does the restart after this menu closes) so the agent truly gets the tools. For anything
	// else (a plain shell) we bind record-only. Returns true when a relaunch was queued.
	attach := func(id string) bool {
		tl := titleOf(id)
		argv, dir, label, key, ok := mx.ActiveLaunch()
		if ok && len(argv) > 0 && isWireableEngine(argv[0]) {
			wiredArgv := wireThreadArgv(argv[0], argv, id)
			fmt.Printf("\n  \x1b[1mRestarting your session\x1b[0m to attach it to \x1b[1m%s\x1b[0m…\n", tl)
			fmt.Println("  \x1b[38;5;245myour conversation carries over; give the agent a moment to reconnect.\x1b[0m")
			fmt.Println("\n  then seed this thread from the conversation so far — run:")
			fmt.Println("  \x1b[36m/mcp__common-ground__seed_from_history\x1b[0m  \x1b[38;5;245m(type / and pick common-ground)\x1b[0m")
			mx.SetPendingReattach(ptymux.Spec{Label: label, Key: key + "·cg", Argv: wiredArgv, Dir: dir, Thread: id, ThreadLabel: tl})
			pause(in)
			return true
		}
		mx.SetActiveThread(id, tl) // shell / non-engine → record-only (the menu writes as you)
		target, wired = id, false
		return false
	}

	for {
		cgFrame("Context Threads")
		if target != "" {
			shared := false
			label := titleOf(target)
			if th, _, err := c.GetThread(target); err == nil && th != nil {
				shared = th.Visibility == "team"
			}
			vis := "private"
			if shared {
				vis = "shared with team"
			}
			if wired {
				fmt.Printf("  attached: \x1b[1m%s\x1b[0m  \x1b[38;5;245m(%s · agent wired)\x1b[0m\n\n", label, vis)
			} else {
				fmt.Printf("  recording to: \x1b[1m%s\x1b[0m  \x1b[38;5;245m(%s · agent not wired)\x1b[0m\n\n", label, vis)
			}
			fmt.Println("    r  record a fact")
			fmt.Println("    v  view shared context")
			if !wired {
				fmt.Println("    a  reconnect the agent (restarts it wired to this thread)")
			}
			fmt.Println("    s  share with a team (or make private)")
			fmt.Println("    w  show web link")
			fmt.Println("    t  attach a different thread")
			fmt.Println("    n  new thread")
		} else {
			fmt.Print("  \x1b[38;5;245mthis session isn't attached to a thread yet\x1b[0m\n\n")
			fmt.Println("    t  attach an existing thread")
			fmt.Println("    n  new thread")
		}
		fmt.Println("    q  back to your session")
		fmt.Print("\n  › ")

		line, _ := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "q", "":
			return
		case "r":
			if target == "" {
				needThread(in)
				continue
			}
			cgRecord(c, in, target)
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
		case "s":
			if target == "" {
				needThread(in)
				continue
			}
			cgShare(c, in, target)
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
	fmt.Printf("\n  \x1b[1m☎  %s\x1b[0m\n", title)
	fmt.Println("  \x1b[38;5;240m────────────────────────────────────────────\x1b[0m")
}

func cgRecord(c *api.Client, in *bufio.Reader, thread string) {
	fmt.Println("\n  kind:  1 decision  2 constraint  3 contract  4 question  5 note")
	fmt.Print("  › ")
	sel, _ := in.ReadString('\n')
	kind := "note"
	switch strings.ToLower(strings.TrimSpace(sel)) {
	case "1", "decision":
		kind = "decision"
	case "2", "constraint":
		kind = "constraint"
	case "3", "contract":
		kind = "contract"
	case "4", "question":
		kind = "question"
	default:
		kind = "note"
	}
	fmt.Printf("  the %s (one line): ", kind)
	body, _ := in.ReadString('\n')
	body = strings.TrimSpace(body)
	if body == "" {
		fmt.Println("  (nothing recorded)")
		pause(in)
		return
	}
	engine := strings.TrimSpace(os.Getenv("PARTYLINE_ENGINE"))
	b, err := c.Remember(thread, kind, body, "", engine, 0)
	if err != nil {
		fmt.Printf("  ✗ %v\n", err)
		pause(in)
		return
	}
	fmt.Printf("  ✓ recorded (%s, #%d) — it's on the web now, and agents can recall it.\n", b.Kind, b.ID)
	pause(in)
}

func cgView(c *api.Client, in *bufio.Reader, thread string) {
	bl, err := c.Recall(thread, 0)
	if err != nil {
		fmt.Printf("\n  ✗ %v\n", err)
		pause(in)
		return
	}
	fmt.Println()
	n := 0
	for _, b := range bl {
		// Same live-only filter agents see (§ safety): hide replaced/pruned/proposed.
		if b.Status == "superseded" || b.Status == "pruned" || b.Status == "proposed" {
			continue
		}
		n++
		tag := ""
		if b.Status == "graduated" {
			tag = " \x1b[38;5;39m↳promoted\x1b[0m"
		}
		fmt.Printf("  \x1b[38;5;245m#%d\x1b[0m [%s]%s %s\n      \x1b[38;5;245m— %s\x1b[0m\n", b.ID, b.Kind, tag, b.Body, b.Author)
	}
	if n == 0 {
		fmt.Println("  (nothing recorded yet)")
	}
	pause(in)
}

// cgShare lets you share the thread with a SPECIFIC team (moving it there) or make it private.
// A team of one (your personal org) shares with nobody, so we always make you pick a real team.
func cgShare(c *api.Client, in *bufio.Reader, thread string) {
	teams := cgTeams(c)
	fmt.Println("\n  share this thread with:")
	fmt.Println("    0) make it private (just you)")
	for i, t := range teams {
		fmt.Printf("    %d) %s\n", i+1, t.Name)
	}
	if len(teams) == 0 {
		fmt.Println("  \x1b[38;5;245m(you're not in any teams — create one with `ptln team create`)\x1b[0m")
	}
	fmt.Print("  › ")
	s, _ := in.ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return
	}
	if n == 0 {
		if e := c.SetThreadVisibility(thread, "private"); e != nil {
			fmt.Printf("  ✗ %v\n", e)
		} else {
			fmt.Println("  ✓ now private")
		}
		pause(in)
		return
	}
	if n < 1 || n > len(teams) {
		return
	}
	if e := c.ShareThreadWithTeam(thread, teams[n-1].Slug); e != nil {
		fmt.Printf("  ✗ %v\n", e)
	} else {
		fmt.Printf("  ✓ shared with %s\n", teams[n-1].Name)
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
		fmt.Println("\n  (no threads yet — use 'n' to make one)")
		pause(in)
		return ""
	}
	fmt.Println()
	limit := len(ths)
	if limit > 20 {
		limit = 20
	}
	for i := 0; i < limit; i++ {
		vis := ""
		if ths[i].Visibility == "team" {
			vis = "  \x1b[38;5;245m· shared\x1b[0m"
		}
		fmt.Printf("  %2d) %s%s\n", i+1, ths[i].Title, vis)
	}
	fmt.Print("\n  number (enter to cancel): ")
	s, _ := in.ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > limit {
		return ""
	}
	return ths[n-1].ID
}

func cgNew(c *api.Client, in *bufio.Reader) string {
	fmt.Print("\n  title: ")
	t, _ := in.ReadString('\n')
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	// Where does it live? Private (your personal space) or shared with a specific team.
	slug, vis := "", ""
	teams := cgTeams(c)
	if len(teams) > 0 {
		fmt.Println("\n  keep it:")
		fmt.Println("    0) private (just you)")
		for i, tm := range teams {
			fmt.Printf("    %d) share with %s\n", i+1, tm.Name)
		}
		fmt.Print("  › ")
		s, _ := in.ReadString('\n')
		if n, e := strconv.Atoi(strings.TrimSpace(s)); e == nil && n >= 1 && n <= len(teams) {
			slug, vis = teams[n-1].Slug, "team"
		}
	}
	th, err := c.CreateThread(t, slug, vis)
	if err != nil || th == nil {
		fmt.Printf("  ✗ %v\n", err)
		pause(in)
		return ""
	}
	where := "private"
	if slug != "" {
		where = "shared with the team"
	}
	fmt.Printf("  ✓ created \"%s\" (%s)\n", th.Title, where)
	return th.ID // caller (attach) wires the session to it
}

func needThread(in *bufio.Reader) {
	fmt.Println("\n  pick a thread (t) or make one (n) first.")
	pause(in)
}

func pause(in *bufio.Reader) {
	fmt.Print("\n  \x1b[38;5;245m(enter to continue)\x1b[0m ")
	_, _ = in.ReadString('\n')
}
