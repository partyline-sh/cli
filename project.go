package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/api"
)

// Common Ground — projects are the durable substrate (a repo/component/product's slow-changing
// truth). Threads attach to projects and graduate facts into their canon. See COMMON-GROUND §3.
//
//	ptln project                     list your team's projects
//	ptln project new "<label>" [--team <slug>]
//	ptln project show <label|id>     the project's canon (graduated facts)
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
	case "visibility":
		projectVisibility(c, args)
	case "repo":
		projectRepo(c, args)
	case "-h", "--help", "help":
		fmt.Println("ptln project — the durable substrate for shared context (Context Threads)")
		fmt.Println("  ptln project                     list your team's projects")
		fmt.Println("  ptln project new \"<label>\" [--team <slug>] [--private]")
		fmt.Println("  ptln project setup [<label>] [--private]")
		fmt.Println("  ptln project repo <label|id> [<remote-url>]   record which repo the project is — defaults to this checkout's origin")
		fmt.Println("                                   set THIS repo up as a project: create it, give it a")
		fmt.Println("                                   thread (pinned in .partyline.json), and register this")
		fmt.Println("                                   directory so your team's agents can build here")
		fmt.Println("                                   unattended (undo: ptln daemon remove-project <label>)")
		fmt.Println("                                   default TEAM-WIDE: teammates see it and their machines")
		fmt.Println("                                   auto-adopt it; --private keeps it yours alone")
		fmt.Println("  ptln project visibility <label> team|private")
		fmt.Println("                                   share a private project with the team, or take a")
		fmt.Println("                                   team project private (creator or instance admin)")
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
	fmt.Printf("%-38s  %-8s  %s\n", "ID", "VIS", "LABEL")
	for _, p := range ps {
		// An older server sends no visibility; every project was team-wide before the field existed.
		vis := p.Visibility
		if vis == "" {
			vis = "team"
		}
		fmt.Printf("%-38s  %-8s  %s\n", p.ID, vis, p.Label)
	}
}

func projectNew(c *api.Client, args []string) {
	label, team, visibility := "", "", ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--team" {
			if i++; i < len(args) {
				team = args[i]
			}
			continue
		}
		if args[i] == "--private" {
			visibility = "private"
			continue
		}
		if label == "" {
			label = args[i]
		}
	}
	if strings.TrimSpace(label) == "" {
		fatal(fmt.Errorf("usage: ptln project new \"<label>\" [--team <slug>] [--private]"))
	}
	p, err := c.CreateProject(label, team, visibility)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("✓ project %s — %q\n", p.ID, p.Label)
	if p.Visibility == "private" {
		fmt.Println("  private: only you can see it — share it later with `ptln project visibility " + p.Label + " team`")
	} else {
		fmt.Println("  team-wide: your teammates can see it, and their machines pick it up automatically")
	}
	fmt.Printf("  attach a thread:   ptln thread attach <thread-id> %s\n", p.ID)
}

// projectVisibility shares a private project with the team, or takes a team project private.
// The server enforces WHO may: the project's creator, or the instance admin.
func projectVisibility(c *api.Client, args []string) {
	if len(args) != 2 || (args[1] != "team" && args[1] != "private") {
		fatal(fmt.Errorf("usage: ptln project visibility <label|id> team|private"))
	}
	ref, want := args[0], args[1]
	if err := c.SetProjectVisibility(resolveProjectRef(c, ref), want); err != nil {
		fatal(err)
	}
	if want == "private" {
		fmt.Printf("✓ %s is now PRIVATE — only you see it; machines that already adopted it keep their local registration\n", ref)
		return
	}
	fmt.Printf("✓ %s is now TEAM-WIDE — teammates see it, and their machines with this repo will adopt it automatically\n", ref)
}

// projectSetupHere is the CLI half of `create_project` (cg_create_project.go): the same four steps,
// the same summary. Every feature needs a CLI path as well as an API one — a setup an agent can do
// but a person cannot is as broken as the reverse.
func projectSetupHere(c *api.Client, args []string) {
	label, visibility := "", ""
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Println("ptln project setup [<label>] [--private] — set the current repo up as a partyline project")
			fmt.Println("  creates the project (label defaults to this directory's name), gives it a context")
			fmt.Println("  thread pinned in .partyline.json, and registers this directory on THIS machine.")
			fmt.Println("  Registration is a grant: your team's agents may build here unattended.")
			fmt.Println("  Default TEAM-WIDE: teammates see the project and their machines that have this")
			fmt.Println("  repo adopt it automatically. --private keeps it yours alone (share later with")
			fmt.Println("  `ptln project visibility <label> team`). Undo registration with")
			fmt.Println("  `ptln daemon remove-project <label>`.")
			return
		}
		if a == "--private" {
			visibility = "private"
			continue
		}
		if label == "" {
			label = a
		}
	}
	set, msg, isErr := createProjectHere(c, label, visibility)
	if isErr || set == nil {
		fatal(fmt.Errorf("%s", msg))
	}
	fmt.Println(msg)

	// The machines step, in the same command. Setting a project up and leaving it runnable on
	// exactly one box was the whole complaint: the CLI had no way to reach the rest of the fleet
	// either, so people set a project up and only discovered later that nothing else could build it.
	setupMachinesInteractive(c, set.Label)
}

// setupMachinesInteractive offers the fleet and binds what the operator picks.
//
// Skipped without a tty (a script, a pipe): choosing machines is a grant, and a grant needs somebody
// present to give it. The non-interactive path prints how to do it instead of guessing.
func setupMachinesInteractive(c *api.Client, label string) {
	machines, err := c.MachineOffers()
	if err != nil {
		fmt.Println("\n  Could not list your machines, so only this one is set up: " + err.Error())
		return
	}
	nodes := setupCandidates(machines, label, "")
	if len(nodes) == 0 {
		return // this machine is already registered; nothing else can offer a directory
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Printf("\n  %d other machine(s) could build %q. Choose them with:\n      ptln project setup %s\n",
			len(nodes), label, label)
		return
	}

	fmt.Printf("\n  WHICH MACHINES SHOULD BE ABLE TO BUILD %q?\n", label)
	fmt.Println("  Enabling one is a GRANT: it declares that directory available for your team's")
	fmt.Println("  agents to build in unattended.")
	fmt.Println()
	for i, n := range nodes {
		how := "would clone it into " + n.Parent + " (a few minutes)"
		if n.Instant() {
			how = "already has this checkout — ready immediately"
		}
		off := ""
		if !n.Online {
			off = "  [offline — picks it up when it reconnects]"
		}
		fmt.Printf("    %d. %s — %s%s\n", i+1, n.Machine, how, off)
	}
	fmt.Print("\n  Numbers to enable (comma-separated), or enter to skip: ")

	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	chosen := pickNodesByNumber(nodes, line)
	if len(chosen) == 0 {
		fmt.Println("  Skipped — this machine can build it; add others later with `ptln project setup`.")
		return
	}
	fmt.Println()
	for _, r := range bindSetupNodes(c, label, chosen) {
		line := "  · " + r.Machine + " — " + r.State
		if r.Reason != "" {
			line += ": " + r.Reason
		}
		fmt.Println(line)
	}
}

// pickNodesByNumber reads a "1,3" style answer. Out-of-range and non-numeric entries are ignored
// rather than guessed at — enabling the wrong machine is a grant given by accident.
func pickNodesByNumber(nodes []setupNodeChoice, answer string) []setupNodeChoice {
	var out []setupNodeChoice
	seen := map[int]bool{}
	for _, f := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' }) {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 || n > len(nodes) || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, nodes[n-1])
	}
	return out
}

func projectShow(c *api.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln project show <label|id>"))
	}
	p, blocks, err := c.GetProject(resolveProjectRef(c, args[0]))
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

// resolveProjectRef turns whatever the operator typed into the id the API wants.
//
// `project ls` prints LABELS, `ptln help` documents `show <label>`, and the endpoint only ever
// accepted a uuid — so the one identifier a person has in front of them was the one that did not
// work, and the answer was "project not found" for a project that plainly exists. A label is
// resolved against the caller's own project list; anything that already looks like an id is passed
// straight through, so a uuid still costs no extra request.
func resolveProjectRef(c *api.Client, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || looksLikeUUID(ref) {
		return ref
	}
	projects, err := c.ListProjects()
	if err != nil {
		return ref // let the API report the failure rather than inventing one here
	}
	if hit := projectWithLabel(projects, ref); hit != nil {
		return hit.ID
	}
	return ref // not a label we know — the API's "not found" is the honest answer
}

// looksLikeUUID is a shape check, not validation: it only decides whether to spend a request
// resolving a label. A malformed id still reaches the API, which is the thing that can judge it.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// projectRepo stamps (or re-stamps) the repository a project belongs to — the linkage that
// makes the project resolvable from a checkout (doctor's "project" line, create_project's
// second-machine dedupe). Exists because the stamp used to happen only at creation, and a
// creation that failed to stamp — an ssh-alias remote, a network blip — left a label nothing
// could resolve, with "fix it in the web app" as the only cure.
func projectRepo(c *api.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: ptln project repo <label|id> [<remote-url>]"))
	}
	remote := ""
	if len(args) > 1 {
		remote = canonicalRemote(args[1], sshResolvedHost)
	} else {
		cwd, _ := os.Getwd()
		remote = gitOriginURL(cwd)
		if remote == "" {
			fatal(fmt.Errorf("no remote given and this directory has no `origin` — run from the checkout, or pass the URL"))
		}
	}
	id := resolveProjectRef(c, args[0])
	if err := c.SetProjectRepoURL(id, remote); err != nil {
		fatal(err)
	}
	fmt.Printf("✓ %s → %s\n", args[0], remote)
	fmt.Println("  checkouts of that repo now resolve to this project (doctor's `project` line proves it)")
}
