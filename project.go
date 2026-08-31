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
	case "setup":
		projectSetupHere(c, args)
	case "show":
		projectShow(c, args)
	case "tools":
		projectTools(c, args)
	case "doc", "document", "globals":
		projectDoc(c, args)
	case "env", "envs", "environments":
		projectEnv(c, args)
	case "-h", "--help", "help":
		fmt.Println("ptln project — the durable substrate for shared context (Context Threads)")
		fmt.Println("  ptln project                     list your team's projects")
		fmt.Println("  ptln project new \"<label>\" [--team <slug>]")
		fmt.Println("  ptln project setup [<label>]     set THIS repo up as a project: create it, give it a")
		fmt.Println("                                   thread (pinned in .partyline.json), and register this")
		fmt.Println("                                   directory so your team's agents can build here")
		fmt.Println("                                   unattended (undo: ptln daemon remove-project <label>)")
		fmt.Println("  ptln project show <id>           the project's promoted facts")
		fmt.Println("  ptln project tools <label>       agent tool grants (planning/build) — view")
		fmt.Println("     … tools <label> --role planning --allow-shell \"gh *\" --allow-mcp <name>")
		fmt.Println("     … tools <label> --role build --revoke-shell \"gh *\"   (edit; review is never grantable)")
		fmt.Println("  ptln project doc <label>         the project document (globals injected into every run)")
		fmt.Println("     … doc <label> --set @BRIEF.md  replace it (- for stdin)")
		fmt.Println("  ptln project env <label>         the deploy chain")
		fmt.Println("     … env <label> --set staging=develop,prod=main")
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

// projectSetupHere is the CLI half of `create_project` (cg_create_project.go): the same four steps,
// the same summary. Every feature needs a CLI path as well as an API one — a setup an agent can do
// but a person cannot is as broken as the reverse.
func projectSetupHere(c *api.Client, args []string) {
	label := ""
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Println("ptln project setup [<label>] — set the current repo up as a partyline project")
			fmt.Println("  creates the project (label defaults to this directory's name), gives it a context")
			fmt.Println("  thread pinned in .partyline.json, and registers this directory on THIS machine.")
			fmt.Println("  Registration is a grant: your team's agents may build here unattended.")
			fmt.Println("  Undo with `ptln daemon remove-project <label>`.")
			return
		}
		if label == "" {
			label = a
		}
	}
	_, msg, isErr := createProjectHere(c, label)
	if isErr {
		fatal(fmt.Errorf("%s", msg))
	}
	fmt.Println(msg)
}

func projectShow(c *api.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln project show <id>"))
	}
	p, blocks, err := c.GetProject(args[0])
	if err != nil {
		fatal(err)
	}
	fmt.Printf("%s  (project)\n", p.Label)
	fmt.Printf("  id      %s\n", p.ID)

	// THE LINKAGE, shown because nothing else showed it. A project has a repo and a context thread,
	// and until now `project show` printed neither — so when a repo resolved to the wrong thread, or
	// to none, no command would tell you. That gap turned a wrong pin into a long hunt: the thread
	// id was visible in .partyline.json and the project was visible here, and nothing connected them.
	if repo := strings.TrimSpace(p.RepoURL); repo != "" {
		fmt.Printf("  repo    %s\n", repo)
	} else {
		fmt.Printf("  repo    (none recorded — this project resolves from no checkout)\n")
	}
	if th, terr := c.ResolveThreadForProject(p.Label); terr != nil {
		fmt.Printf("  thread  (could not resolve: %s)\n", terr.Error())
	} else if th == nil {
		fmt.Printf("  thread  (none)\n")
	} else {
		fmt.Printf("  thread  %s  %s\n", th.ID, th.Title)
	}

	fmt.Println()
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
