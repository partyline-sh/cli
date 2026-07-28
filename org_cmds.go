// org subcommands — thin clients of /api/v1 for your ONE org's members. Single-org model: every
// user has exactly one org, so there's no --team, no disambiguation, and no creating a second org.
// `team` stays as an alias for `org`. Member management here mirrors the web (RBAC).
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

// myOrgSlug returns the caller's one org (single-org model) — no flag, no disambiguation.
func myOrgSlug(c *api.Client) string {
	slug, err := c.DefaultOrgSlug()
	if err != nil {
		fatal(err)
	}
	if slug == "" {
		fatal(fmt.Errorf("no org found for your account"))
	}
	return slug
}

func teamMain(args []string) {
	c := mustClient()
	if len(args) == 0 {
		args = []string{"members"}
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("org", flag.ExitOnError)

	switch sub {
	case "members", "list":
		_ = fs.Parse(rest)
		ms, err := c.OrgMembers(myOrgSlug(c))
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
			fatal(fmt.Errorf("usage: ptln org invite <email> [--role member|admin]"))
		}
		if err := c.InviteOrg(myOrgSlug(c), fs.Arg(0), *role); err != nil {
			fatal(err)
		}
		fmt.Printf("✉️  invited %s\n", fs.Arg(0))
	case "access":
		_ = fs.Parse(rest)
		if fs.NArg() < 2 {
			fatal(fmt.Errorf("usage: ptln org access <handle|email> full|viewer"))
		}
		who, access := fs.Arg(0), strings.ToLower(fs.Arg(1))
		if access != "full" && access != "viewer" {
			fatal(fmt.Errorf("access must be 'full' or 'viewer'"))
		}
		slug := myOrgSlug(c)
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
			fatal(fmt.Errorf("no member %q in your org — try `ptln org members`", who))
		}
		if err := c.SetMemberAccess(slug, uid, access); err != nil {
			fatal(err)
		}
		fmt.Printf("🔓 set %s to %s access\n", label, access)
		if access == "full" {
			fmt.Println("they'll pick it up when they (re)join the session — then the host grants typing with /pgrant or ctrl-\\ g")
		}
	default:
		fatal(fmt.Errorf("unknown: ptln org %s (members|invite|access)", sub))
	}
}
