package main

import (
	"bufio"
	"fmt"
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
	in := stdin()
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
		fmt.Printf("    2) join a running party  %s\n", dim(fmt.Sprintf("(%d available)", len(active))))
	}
	sel, ok := Input("number", "1")
	if !ok {
		return
	}
	switch sel {
	case "2":
		if len(active) > 0 {
			partyJoinMenu(c, in, active)
			return
		}
		fallthrough
	case "1":
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
	fmt.Printf("    web:   %s   %s\n", partyWebHome(out.Link), dim("← people join here"))
	fmt.Printf("    code:  %s\n", out.JoinCode)
	fmt.Printf("    agent: %s\n", dim(fmt.Sprintf("ptln party '%s' --name <name>", out.Link)))
	bringAgent(in, out.Link)
}

func partyJoinMenu(c *api.Client, in *bufio.Reader, active []api.ActiveParty) {
	fmt.Println("\n  running parties:")
	n, ok := Pick("number", active, func(p api.ActiveParty) string {
		row := fmt.Sprintf("%-10s %s", p.Mode, dim(p.OrgName))
		if p.IsMine {
			row += "  " + dim("· yours")
		}
		return row
	})
	if !ok {
		return
	}
	p := active[n]

	// Mint the join link now so we can print/use it; the agent's name doubles as the token label.
	fmt.Println()
	name, ok := Input("bring an agent — name", "")
	if !ok {
		return
	}
	link, err := c.JoinParty(p.ID, name)
	if err != nil || link == "" {
		fmt.Printf("  ✗ couldn't get a join link: %v\n", err)
		return
	}
	engine, model, role, ok := askAgentDetails()
	if !ok {
		return
	}
	runPartyAgent(link, name, engine, model, role)
}

// bringAgent offers to spawn one agent into a just-created party. Enter skips.
func bringAgent(in *bufio.Reader, link string) {
	fmt.Println()
	name, ok := Input("bring an agent now? name", "skip")
	if !ok || name == "skip" {
		fmt.Println("  done — share the web link, or run the agent command above.")
		return
	}
	engine, model, role, ok := askAgentDetails()
	if !ok {
		return
	}
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
	n, ok := Pick("number", orgs, teamLabel)
	if !ok {
		return ""
	}
	return orgs[n].Slug
}

func pickMode(in *bufio.Reader) string {
	fmt.Println("\n  mode:")
	for i, m := range partyModeOpts {
		fmt.Printf("    %s  %-10s %s\n", sgr(cgKey, fmt.Sprintf("%2d", i+1)), m.key, dim(m.blurb))
	}
	t, ok := Input("number or mode name", "")
	if !ok {
		return ""
	}
	if n, err := strconv.Atoi(t); err == nil && n >= 1 && n <= len(partyModeOpts) {
		return partyModeOpts[n-1].key
	}
	for _, m := range partyModeOpts { // allow typing the key
		if strings.EqualFold(t, m.key) {
			return m.key
		}
	}
	fmt.Printf("  %s\n", dim("(not a mode — cancelled)"))
	return ""
}

func teamLabel(o api.Org) string {
	if o.Personal {
		return o.Name + "  " + dim("(personal)")
	}
	return o.Name + "  " + dim("("+o.Slug+")")
}

// askAgentDetails collects the three agent knobs. ok=false means the operator cancelled at any of
// them — the caller must NOT go on to spawn an agent it was told to stop asking about.
func askAgentDetails() (engine, model, role string, ok bool) {
	if engine, ok = Input("engine", "claude"); !ok {
		return "", "", "", false
	}
	if model, ok = Input("model", "engine default"); !ok {
		return "", "", "", false
	}
	if model == "engine default" {
		model = ""
	}
	if role, ok = Input("role", "none"); !ok {
		return "", "", "", false
	}
	if role == "none" {
		role = ""
	}
	return engine, model, role, true
}
