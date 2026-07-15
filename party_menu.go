package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// `ptln party` with no arguments opens this interactive launcher — pick a team, pick a mode, and
// it creates the party and (optionally) brings an agent in, with no partyline.yml to author. It's
// the friendly front door to the power path (`ptln party up <file>` for a whole declared room).

type partyModeOpt struct {
	key, blurb string
}

var partyModeOpts = []partyModeOpt{
	{"chat", "one helpful agent, casual conversation"},
	{"fix", "describe a bug in plain words → a code-aware agent refines it into a reviewable PR"},
	{"approach", "grounded experts take cited positions — you make the call"},
	{"prd", "draft and pressure-test a spec together"},
	{"incident", "coordinate a live incident, action-oriented"},
	{"brainstorm", "agents argue distinct positions, no brake"},
}

func partyMenu() {
	in := bufio.NewReader(os.Stdin)
	if api.LoadToken() == "" {
		fmt.Println("You're not logged in — run `ptln login` first.")
		return
	}
	c := api.New()
	active, _ := c.ListParties() // best-effort; empty on error or older backend

	fmt.Println("\n  \x1b[1m☎  Party\x1b[0m")
	fmt.Println("  ─────────────────────────────────────────────")
	fmt.Println("    1) start a new party")
	if len(active) > 0 {
		fmt.Printf("    2) join a running party  \x1b[38;5;245m(%d available)\x1b[0m\n", len(active))
	}
	fmt.Println("    q) cancel")
	fmt.Print("  › ")
	sel, _ := in.ReadString('\n')
	switch strings.TrimSpace(sel) {
	case "2":
		if len(active) > 0 {
			partyJoinMenu(c, in, active)
			return
		}
		fallthrough
	case "1", "":
		partyCreateMenu(c, in)
	default:
		return
	}
}

func partyCreateMenu(c *api.Client, in *bufio.Reader) {
	slug := pickTeam(c, in)
	if slug == "" {
		return
	}
	mode := pickMode(in)
	if mode == "" {
		return
	}
	out, err := c.CreateParty(slug, mode)
	if err != nil {
		fmt.Printf("\n  ✗ couldn't create the party: %v\n", err)
		return
	}
	fmt.Printf("\n  ✓ Party created (%s mode) for %s\n", mode, slug)
	fmt.Printf("    web:   %s   \x1b[38;5;245m← people join here\x1b[0m\n", partyWebHome(out.Link))
	fmt.Printf("    code:  %s\n", out.JoinCode)
	fmt.Printf("    agent: \x1b[38;5;245mptln party '%s' --name <name>\x1b[0m\n", out.Link)
	bringAgent(in, out.Link)
}

func partyJoinMenu(c *api.Client, in *bufio.Reader, active []api.ActiveParty) {
	fmt.Println("\n  running parties:")
	for i, p := range active {
		mine := ""
		if p.IsMine {
			mine = "  \x1b[38;5;245m· yours\x1b[0m"
		}
		fmt.Printf("    %d) %-10s \x1b[38;5;245m%s\x1b[0m%s\n", i+1, p.Mode, p.OrgName, mine)
	}
	fmt.Print("  › ")
	s, _ := in.ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > len(active) {
		fmt.Println("  (cancelled)")
		return
	}
	p := active[n-1]

	// Mint the join link now so we can print/use it; the agent's name doubles as the token label.
	fmt.Print("\n  bring an agent — name: ")
	name, _ := in.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		fmt.Println("  (cancelled)")
		return
	}
	link, err := c.JoinParty(p.ID, name)
	if err != nil || link == "" {
		fmt.Printf("  ✗ couldn't get a join link: %v\n", err)
		return
	}
	engine := ask(in, "  engine [claude]: ", "claude")
	model := ask(in, "  model (enter for default): ", "")
	role := ask(in, "  role (enter for none): ", "")
	runPartyAgent(link, name, engine, model, role)
}

// bringAgent offers to spawn one agent into a just-created party. Enter skips.
func bringAgent(in *bufio.Reader, link string) {
	fmt.Print("\n  bring an agent now? name (enter to skip): ")
	name, _ := in.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		fmt.Println("  done — share the web link, or run the agent command above.")
		return
	}
	engine := ask(in, "  engine [claude]: ", "claude")
	model := ask(in, "  model (enter for default): ", "")
	role := ask(in, "  role (enter for none): ", "")
	runPartyAgent(link, name, engine, model, role)
}

// runPartyAgent joins the party as a live agent in THIS terminal (Ctrl-C to leave), reusing the
// full runner path.
func runPartyAgent(link, name, engine, model, role string) {
	args := []string{link, "--name", name, "--engine", engine}
	if model != "" {
		args = append(args, "--model", model)
	}
	if role != "" {
		args = append(args, "--role", role)
	}
	fmt.Printf("\n  bringing %s in (%s)… Ctrl-C to leave.\n\n", name, engine)
	partyMain(args)
}

// pickTeam lists the caller's teams; auto-selects when there's only one. Returns "" on cancel.
func pickTeam(c *api.Client, in *bufio.Reader) string {
	orgs, err := c.ListOrgs()
	if err != nil || len(orgs) == 0 {
		fmt.Printf("\n  ✗ no teams found (%v) — create one with `ptln team`.\n", err)
		return ""
	}
	if len(orgs) == 1 {
		fmt.Printf("  team:  %s\n", teamLabel(orgs[0]))
		return orgs[0].Slug
	}
	fmt.Println("\n  team:")
	for i, o := range orgs {
		fmt.Printf("    %d) %s\n", i+1, teamLabel(o))
	}
	fmt.Print("  › ")
	s, _ := in.ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > len(orgs) {
		fmt.Println("  (cancelled)")
		return ""
	}
	return orgs[n-1].Slug
}

func pickMode(in *bufio.Reader) string {
	fmt.Println("\n  mode:")
	for i, m := range partyModeOpts {
		fmt.Printf("    %d) %-10s \x1b[38;5;245m%s\x1b[0m\n", i+1, m.key, m.blurb)
	}
	fmt.Print("  › ")
	s, _ := in.ReadString('\n')
	t := strings.TrimSpace(s)
	if n, err := strconv.Atoi(t); err == nil && n >= 1 && n <= len(partyModeOpts) {
		return partyModeOpts[n-1].key
	}
	for _, m := range partyModeOpts { // allow typing the key
		if strings.EqualFold(t, m.key) {
			return m.key
		}
	}
	fmt.Println("  (cancelled)")
	return ""
}

func teamLabel(o api.Org) string {
	if o.Personal {
		return o.Name + "  \x1b[38;5;245m(personal)\x1b[0m"
	}
	return fmt.Sprintf("%s  \x1b[38;5;245m(%s)\x1b[0m", o.Name, o.Slug)
}

func ask(in *bufio.Reader, prompt, def string) string {
	fmt.Print(prompt)
	s, _ := in.ReadString('\n')
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}
