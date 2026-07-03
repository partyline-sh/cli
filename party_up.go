package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"partyline.sh/partyline/internal/api"
)

// `ptln party up` — bring up a whole room of agents from a declarative party file.
// See docs/PARTY-UP.md. Slice 1: parse the file, create the party (backend), and
// print the per-agent launch commands (+ --dry-run that creates nothing). Slice 2
// spawns + supervises the runners; Slice 3 adds ssh transport for host: agents.

type partyFile struct {
	Party struct {
		Team string `yaml:"team"`
		Mode string `yaml:"mode"`
		Name string `yaml:"name"`
	} `yaml:"party"`
	Agents []agentSpec `yaml:"agents"`
}

type agentSpec struct {
	Name   string   `yaml:"name"`
	Role   string   `yaml:"role"`
	Engine string   `yaml:"engine"`
	Model  string   `yaml:"model"`
	Dir    string   `yaml:"dir"`
	Resume string   `yaml:"resume"`
	Host   string   `yaml:"host"` // ssh there instead of running locally (same LAN)
	Flags  []string `yaml:"flags"`
}

func partyUp(args []string) {
	file := "partyline.yml"
	dryRun := false
	teamOverride := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			dryRun = true
		case "--team":
			i++
			if i < len(args) {
				teamOverride = args[i]
			}
		case "-h", "--help":
			partyUpUsage()
			return
		default:
			if !strings.HasPrefix(args[i], "-") {
				file = args[i]
			}
		}
	}

	data, err := os.ReadFile(file)
	if err != nil {
		fatal(fmt.Errorf("party up: can't read %s (see docs/PARTY-UP.md for the format): %w", file, err))
	}
	var pf partyFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		fatal(fmt.Errorf("party up: %s isn't valid YAML: %w", file, err))
	}
	if teamOverride != "" {
		pf.Party.Team = teamOverride
	}
	mode := pf.Party.Mode
	if mode == "" {
		mode = "chat"
	}
	if pf.Party.Team == "" {
		fatal(fmt.Errorf("party up: party.team is required (your team/org slug — see `ptln team`)"))
	}
	if len(pf.Agents) == 0 {
		fatal(fmt.Errorf("party up: no agents defined in %s", file))
	}
	for _, a := range pf.Agents {
		if strings.TrimSpace(a.Name) == "" {
			fatal(fmt.Errorf("party up: every agent needs a name"))
		}
	}

	if dryRun {
		fmt.Printf("party up (dry run): would create a %q party for team %q with %d agent(s)\n\n", mode, pf.Party.Team, len(pf.Agents))
		for _, a := range pf.Agents {
			fmt.Printf("  • %-14s %s\n      %s\n", a.Name, partyTransport(a), partyAgentCmd(a, "<join-link>"))
		}
		fmt.Println("\nRun without --dry-run to create the party.")
		return
	}

	out, err := api.New().CreateParty(pf.Party.Team, mode)
	if err != nil {
		fatal(fmt.Errorf("party up: couldn't create the party (logged in? `ptln login`): %w", err))
	}
	fmt.Printf("✓ Party created (%s mode) for %s\n", mode, pf.Party.Team)
	fmt.Printf("  web:  %s\n  code: %s\n\n", partyWebHome(out.Link), out.JoinCode)
	// Slice 1 prints the launch commands; Slice 2 will spawn + supervise these.
	fmt.Println("Bring the agents — run each (S2 will auto-spawn these):")
	for _, a := range pf.Agents {
		fmt.Printf("  # %s  %s\n  %s\n", a.Name, partyTransport(a), partyAgentCmd(a, out.Link))
	}
	fmt.Printf("\nOn a different network? That operator joins with:\n  ptln party '%s' --name <name>\n", out.Link)
}

// partyAgentCmd builds the `ptln party …` command that brings one agent in. Display
// only in Slice 1 (Slice 2 execs the equivalent argv directly; Slice 3 wraps host:
// agents in ssh).
func partyAgentCmd(a agentSpec, link string) string {
	var b strings.Builder
	if a.Dir != "" {
		fmt.Fprintf(&b, "cd %s && ", shQuote(expandTilde(a.Dir)))
	}
	fmt.Fprintf(&b, "ptln party '%s' --name %s", link, a.Name)
	if a.Role != "" {
		fmt.Fprintf(&b, " --role %s", shQuote(a.Role))
	}
	if a.Engine != "" && a.Engine != "claude" {
		fmt.Fprintf(&b, " --engine %s", a.Engine)
	}
	if a.Model != "" {
		fmt.Fprintf(&b, " --model %s", a.Model)
	}
	if a.Resume != "" {
		fmt.Fprintf(&b, " --resume %s", a.Resume)
	}
	for _, f := range a.Flags {
		fmt.Fprintf(&b, " %s", shQuote(f))
	}
	cmd := b.String()
	if a.Host != "" {
		cmd = "ssh " + a.Host + " " + shQuote(cmd)
	}
	return cmd
}

func partyTransport(a agentSpec) string {
	if a.Host != "" {
		return "[ssh " + a.Host + "]"
	}
	return "[local]"
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	return p
}

// partyWebHome strips the #token fragment from a join link → the shareable web URL.
func partyWebHome(link string) string {
	if i := strings.Index(link, "#"); i >= 0 {
		return link[:i]
	}
	return link
}

func partyUpUsage() {
	fmt.Println(`Usage: ptln party up [partyline.yml] [--dry-run] [--team <slug>]

Create a party and bring in a whole room of agents from a declarative file
(docs/PARTY-UP.md). --dry-run prints the plan without creating anything.`)
}
