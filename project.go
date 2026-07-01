package main

import (
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// Common Ground — projects are the durable substrate (a repo/component/product's slow-changing
// truth). Threads attach to projects and graduate facts into their canon. See COMMON-GROUND §3.
//
//	ptln project                     list your team's projects
//	ptln project new "<label>" [--team <slug>]
//	ptln project show <id>           the project's canon (graduated facts)
func projectMain(args []string) {
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
		projectList(c)
	case "new":
		projectNew(c, args)
	case "show":
		projectShow(c, args)
	case "-h", "--help", "help":
		fmt.Println("ptln project — the durable substrate for shared context (Common Ground)")
		fmt.Println("  ptln project                     list your team's projects")
		fmt.Println("  ptln project new \"<label>\" [--team <slug>]")
		fmt.Println("  ptln project show <id>           the canon (graduated facts)")
		fmt.Println("  graduate a thread fact into a project:  ptln thread graduate <thread> <#> <project>")
	default:
		fatal(fmt.Errorf("unknown: ptln project %q — run `ptln project help`", sub))
	}
}

func projectList(c *api.Client) {
	ps, err := c.ListProjects()
	if err != nil {
		fatal(err)
	}
	if len(ps) == 0 {
		fmt.Println("no projects yet — `ptln project new \"<label>\"`")
		return
	}
	fmt.Printf("%-38s  %s\n", "ID", "LABEL")
	for _, p := range ps {
		fmt.Printf("%-38s  %s\n", p.ID, p.Label)
	}
}

func projectNew(c *api.Client, args []string) {
	label, team := "", ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--team" {
			if i++; i < len(args) {
				team = args[i]
			}
			continue
		}
		if label == "" {
			label = args[i]
		}
	}
	if strings.TrimSpace(label) == "" {
		fatal(fmt.Errorf("usage: ptln project new \"<label>\" [--team <slug>]"))
	}
	p, err := c.CreateProject(label, team)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ project %s — %q\n", p.ID, p.Label)
	fmt.Printf("  attach a thread:   ptln thread attach <thread-id> %s\n", p.ID)
}

func projectShow(c *api.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln project show <id>"))
	}
	p, blocks, err := c.GetProject(args[0])
	if err != nil {
		fatal(err)
	}
	fmt.Printf("%s  (canon)\n", p.Label)
	if len(blocks) == 0 {
		fmt.Println("  (nothing graduated yet — `ptln thread graduate <thread> <#> " + p.ID + "`)")
		return
	}
	for _, b := range blocks {
		tag := ""
		if b.Status == "superseded" {
			tag = " [superseded]"
		}
		fmt.Printf("  • #%-3d %-10s %s%s\n", b.ID, b.Kind, b.Body, tag)
		fmt.Printf("        %s\n", b.Author)
	}
}
