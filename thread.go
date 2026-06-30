package main

import (
	"fmt"
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
	fmt.Println("  ptln thread remember <id> <kind> \"<fact>\"   kinds: decision|constraint|contract|question|note")
	fmt.Println("  ptln thread recall <id>          everything recorded on the thread")
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
	fmt.Printf("  attach a session:  ptln llms new <tool> --thread %s\n", t.ID)
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
		fmt.Printf("  • %-10s %s%s\n", b.Kind, b.Body, tag)
		fmt.Printf("    %s · %s\n", b.Author, humanAge(parseTime(b.CreatedAt)))
	}
}

func threadRemember(c *api.Client, args []string) {
	if len(args) < 3 {
		fatal(fmt.Errorf("usage: ptln thread remember <id> <decision|constraint|contract|question|note> \"<fact>\""))
	}
	id, kind, body := args[0], args[1], strings.Join(args[2:], " ")
	b, err := c.Remember(id, kind, body, "", "", 0)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ remembered (%s, #%d)\n", b.Kind, b.ID)
}
