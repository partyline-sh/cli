package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// `ptln key` — team API keys from a shell.
//
// A key is what lets something that is not a person call the API: CI, a script, a service. Minting
// one was previously a web-only act, which meant any automation an agent set up ended with "now go
// to Settings and make a key" — the exact handoff this epic exists to remove.
//
// The key is shown ONCE. `--key-only` is the safest way to consume it: the value goes straight into
// whatever stores it, never through a transcript or a clipboard.

func keyCmd(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		keyHelp()
		return
	}
	switch args[0] {
	case "ls", "list":
		keyList(args[1:])
	case "create", "new", "add", "mint":
		keyCreate(args[1:])
	case "revoke", "rm", "delete":
		keyRevoke(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "ptln key: unknown command %q\n\n", args[0])
		keyHelp()
		os.Exit(2)
	}
}

func keyHelp() {
	fmt.Print(`Usage: ptln key <command>

API keys for things that are not people: CI, scripts, services.

COMMANDS
  ls                    keys on your team (--json for machine output)
  create <name>         mint one — prints the key ONCE
  revoke <name|id>      stop it working (the record stays, so you can see why it stopped)

CREATE FLAGS
  --scopes <list>       comma-separated scopes; omit for the default set
  --expires <date>      RFC3339, e.g. 2027-01-01T00:00:00Z (default: no expiry)
  --key-only            print ONLY the key on stdout, so it can be piped:
                          ptln key create ci --key-only | gh secret set PARTYLINE_API_KEY
  --json                print the created key as JSON

Only owners and admins can mint a team key.
`)
}

func keyList(args []string) {
	creds, err := settingsClient().ListCredentials()
	if err != nil {
		fatal(err)
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(creds, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(creds) == 0 {
		fmt.Println("No API keys.\n\nMint one:  ptln key create ci --key-only | gh secret set PARTYLINE_API_KEY")
		return
	}
	for _, c := range creds {
		state := "active"
		if c.RevokedAt != "" {
			// Revocation is a tombstone rather than a delete precisely so this line can exist:
			// "this key was revoked" is the answer to "why did it stop working".
			state = "revoked"
		}
		used := "never used"
		if c.LastUsed != "" {
			used = "last used " + clip(c.LastUsed, 10)
		}
		fmt.Printf("%-22s %-14s %-10s %-8s %s\n", clip(c.Name, 22), c.Prefix+"…", c.Kind, state, used)
	}
}

func keyCreate(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fatal(fmt.Errorf("ptln key create needs a name: ptln key create ci --key-only"))
	}
	name := args[0]
	rest := args[1:]

	var scopes []string
	if s := flagVal(rest, "--scopes"); s != "" {
		for _, x := range strings.Split(s, ",") {
			if x = strings.TrimSpace(x); x != "" {
				scopes = append(scopes, x)
			}
		}
	}

	id, secret, err := settingsClient().CreateCredential(name, scopes, flagVal(rest, "--expires"))
	if err != nil {
		fatal(err)
	}

	if hasFlag(rest, "--key-only") {
		fmt.Fprintf(os.Stderr, "Minted key %q\n", name)
		fmt.Println(secret)
		return
	}
	if hasFlag(rest, "--json") {
		b, _ := json.MarshalIndent(map[string]any{"id": id, "name": name, "key": secret}, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("Minted key %q (shown once — store it now):\n\n  %s\n\nUse it as a bearer token:\n  curl -H \"Authorization: Bearer $KEY\" %s/api/v1/me\n", name, secret, apiBase())
}

func keyRevoke(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("ptln key revoke needs a name or id (see: ptln key ls)"))
	}
	creds, err := settingsClient().ListCredentials()
	if err != nil {
		fatal(err)
	}
	for _, c := range creds {
		if c.ID == args[0] || strings.EqualFold(c.Name, args[0]) {
			if c.RevokedAt != "" {
				fmt.Printf("%s was already revoked.\n", c.Name)
				return
			}
			if err := settingsClient().RevokeCredential(c.ID); err != nil {
				fatal(err)
			}
			fmt.Printf("Revoked %s. Anything using it starts getting 401s now.\n", c.Name)
			return
		}
	}
	names := make([]string, 0, len(creds))
	for _, c := range creds {
		if c.RevokedAt == "" {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		fatal(fmt.Errorf("no active keys on this team"))
	}
	fatal(fmt.Errorf("no key called %q. Active keys: %s", args[0], strings.Join(names, ", ")))
}
