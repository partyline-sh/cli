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
	// The thread this session is attached to: the mux tracks it per-session (set at launch or
	// here), falling back to the launch env. Picking/creating below attaches it — no relaunch.
	target := mx.ActiveThread()
	if target == "" {
		target = strings.TrimSpace(os.Getenv("PARTYLINE_THREAD_ID"))
	}
	attach := func(id string) {
		target = id
		mx.SetActiveThread(id) // sticks to this session: the menu + the checkup now follow it
	}

	for {
		cgFrame("Context Threads")
		if target != "" {
			label := target
			shared := false
			if th, _, err := c.GetThread(target); err == nil && th != nil {
				label = th.Title
				shared = th.Visibility == "team"
			}
			vis := "private"
			if shared {
				vis = "shared with team"
			}
			fmt.Printf("  attached: \x1b[1m%s\x1b[0m  \x1b[38;5;245m(%s)\x1b[0m\n\n", label, vis)
			fmt.Println("    r  record a fact")
			fmt.Println("    v  view shared context")
			fmt.Println("    s  toggle team sharing")
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
		case "s":
			if target == "" {
				needThread(in)
				continue
			}
			cgToggleShare(c, in, target)
		case "w":
			if target == "" {
				needThread(in)
				continue
			}
			fmt.Printf("\n  %s/threads/%s\n", api.Base(), target)
			pause(in)
		case "t":
			if id := cgPick(c, in); id != "" {
				attach(id)
			}
		case "n":
			if id := cgNew(c, in); id != "" {
				attach(id)
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

func cgToggleShare(c *api.Client, in *bufio.Reader, thread string) {
	th, _, err := c.GetThread(thread)
	if err != nil || th == nil {
		fmt.Printf("\n  ✗ can't read thread\n")
		pause(in)
		return
	}
	next, word := "team", "shared with your team"
	if th.Visibility == "team" {
		next, word = "private", "private"
	}
	if err := c.SetThreadVisibility(thread, next); err != nil {
		fmt.Printf("\n  ✗ %v\n", err)
		pause(in)
		return
	}
	fmt.Printf("\n  ✓ now %s\n", word)
	pause(in)
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
	th, err := c.CreateThread(t, "", "")
	if err != nil || th == nil {
		fmt.Printf("  ✗ %v\n", err)
		pause(in)
		return ""
	}
	engine := strings.TrimSpace(os.Getenv("PARTYLINE_ENGINE"))
	if engine == "" {
		engine = "claude"
	}
	fmt.Printf("  ✓ created \"%s\"\n", th.Title)
	fmt.Printf("  \x1b[38;5;245mto give a new agent the recall/remember tools, launch it wired:\n      ptln new %s --thread %s\x1b[0m\n", engine, th.ID)
	pause(in)
	return th.ID
}

func needThread(in *bufio.Reader) {
	fmt.Println("\n  pick a thread (t) or make one (n) first.")
	pause(in)
}

func pause(in *bufio.Reader) {
	fmt.Print("\n  \x1b[38;5;245m(enter to continue)\x1b[0m ")
	_, _ = in.ReadString('\n')
}
