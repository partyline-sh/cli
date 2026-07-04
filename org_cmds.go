// team subcommands — thin clients of /api/v1 (same routes as the web). A "team"
// is a non-personal org; routes stay /api/v1/orgs. `org` is kept as an alias.
package main

import (
	"flag"
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/api"
)

func mustClient() *api.Client {
	c := api.New()
	if c.Token == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login`"))
	}
	return c
}

// resolveTeam returns the --team (or --org alias) slug, or the caller's single
// team if there's exactly one, else errors asking which team.
func resolveTeam(c *api.Client, fs *flag.FlagSet) string {
	if v := fs.Lookup("team").Value.String(); v != "" {
		return v
	}
	if v := fs.Lookup("org").Value.String(); v != "" {
		return v
	}
	orgs, err := c.ListOrgs()
	if err != nil {
		fatal(err)
	}
	var teams []api.Org
	for _, o := range orgs {
		if !o.Personal {
			teams = append(teams, o)
		}
	}
	switch len(teams) {
	case 1:
		return teams[0].Slug
	case 0:
		fatal(fmt.Errorf("you have no teams — create one: ptln team create <name>"))
	default:
		fatal(fmt.Errorf("you're on multiple teams — pass --team <slug> (see `ptln team`)"))
	}
	return ""
}

func teamMain(args []string) {
	c := mustClient()
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("team", flag.ExitOnError)
	fs.String("team", "", "team slug")
	fs.String("org", "", "alias for --team")

	switch sub {
	case "list":
		_ = fs.Parse(rest)
		orgs, err := c.ListOrgs()
		if err != nil {
			fatal(err)
		}
		for _, o := range orgs {
			label := o.Name
			if o.Personal {
				label = "Personal"
			}
			fmt.Printf("%-24s %-8s %s\n", o.Slug, o.Role, label)
		}
	case "create":
		_ = fs.Parse(rest)
		if fs.NArg() < 1 {
			fatal(fmt.Errorf("usage: ptln team create <name>"))
		}
		o, err := c.CreateOrg(fs.Arg(0))
		if err != nil {
			fatal(err)
		}
		fmt.Printf("👥 created team %s (%s)\n", o.Name, o.Slug)
	case "members":
		_ = fs.Parse(rest)
		slug := resolveTeam(c, fs)
		ms, err := c.OrgMembers(slug)
		if err != nil {
			fatal(err)
		}
		for _, m := range ms {
			who := m.DisplayName
			if who == "" {
				who = m.Email
			}
			fmt.Printf("%-8s %-28s %s\n", m.Role, who, m.Email)
		}
	case "invite":
		role := fs.String("role", "member", "member|admin")
		_ = fs.Parse(rest)
		if fs.NArg() < 1 {
			fatal(fmt.Errorf("usage: ptln team invite <email> [--team slug] [--role member|admin]"))
		}
		slug := resolveTeam(c, fs)
		if err := c.InviteOrg(slug, fs.Arg(0), *role); err != nil {
			fatal(err)
		}
		fmt.Printf("✉️  invited %s to %s\n", fs.Arg(0), slug)
	case "access":
		_ = fs.Parse(rest)
		if fs.NArg() < 2 {
			fatal(fmt.Errorf("usage: ptln team access <handle|email> full|viewer [--team slug]"))
		}
		who, access := fs.Arg(0), strings.ToLower(fs.Arg(1))
		if access != "full" && access != "viewer" {
			fatal(fmt.Errorf("access must be 'full' or 'viewer'"))
		}
		slug := resolveTeam(c, fs)
		ms, err := c.OrgMembers(slug)
		if err != nil {
			fatal(err)
		}
		var uid, label string
		for _, m := range ms {
			if strings.EqualFold(m.Handle, who) || strings.EqualFold(m.Email, who) || strings.EqualFold(m.DisplayName, who) {
				uid, label = m.UserID, m.Email
				break
			}
		}
		if uid == "" {
			fatal(fmt.Errorf("no member %q in %s — try `ptln team members`", who, slug))
		}
		if err := c.SetMemberAccess(slug, uid, access); err != nil {
			fatal(err)
		}
		fmt.Printf("🔓 set %s to %s access in %s\n", label, access, slug)
		if access == "full" {
			fmt.Println("they'll pick it up when they (re)join the session — then the host grants typing with /pgrant or ctrl-\\ g")
		}
	default:
		fatal(fmt.Errorf("unknown: ptln team %s (list|create|members|invite|access)", sub))
	}
}
