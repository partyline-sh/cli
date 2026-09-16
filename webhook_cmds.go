package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// `ptln webhook` — where a team's events go, from a shell.
//
// The outbound sibling of `ptln trigger`. Triggers are how the outside world starts work here;
// webhooks are how work here tells the outside world. Until this existed the deploy story was only
// half configurable from a shell: an agent could create the entry point but not the exit.
//
// Same secret discipline as triggers — see the --key-only note in trigger_cmds.go. The signing
// secret is shown once and never again, so it is worth piping rather than reading.

func webhookCmd(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		webhookHelp()
		return
	}
	switch args[0] {
	case "ls", "list":
		webhookList(args[1:])
	case "add", "create", "new":
		webhookAdd(args[1:])
	case "rm", "delete":
		webhookRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "ptln webhook: unknown command %q\n\n", args[0])
		webhookHelp()
		os.Exit(2)
	}
}

func webhookHelp() {
	fmt.Print(`Usage: ptln webhook <command>

Outbound: partyline POSTs to your URL when something happens here. Every delivery
is signed with a secret shown once at creation, which the receiver uses to verify.

COMMANDS
  ls                    endpoints on your team, and the events you can subscribe to
  add <name> <url>      add one (https only, public address)
  rm <name|id>          remove it — deliveries stop immediately

ADD FLAGS
  --on <kinds>          comma-separated events; omit for ALL of them
                        e.g. --on run.failed,run.needs_approval
  --key-only            print ONLY the signing secret on stdout, so it can be piped:
                          ptln webhook add CI https://ci.example.com/hook --key-only \
                            | gh secret set PARTYLINE_WEBHOOK_SECRET
  --json                print the created endpoint as JSON

Only owners and admins can add a webhook — it is an outbound data path, not a preference.
`)
}

func webhookList(args []string) {
	eps, kinds, err := settingsClient().ListWebhooks()
	if err != nil {
		fatal(err)
	}
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(map[string]any{"endpoints": eps, "kinds": kinds}, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(eps) == 0 {
		fmt.Printf("No webhook endpoints.\n\nAdd one:  ptln webhook add \"CI\" https://example.com/hook --on run.failed\nEvents:   %s\n", strings.Join(kinds, ", "))
		return
	}
	for _, e := range eps {
		on := "all events"
		if len(e.Kinds) > 0 {
			on = strings.Join(e.Kinds, ",")
		}
		state := ""
		if !e.Enabled {
			// An endpoint disables itself after repeated failures. Saying so is the difference
			// between "why did this stop" and a silent dead integration.
			state = "  (off — too many failures)"
		}
		fmt.Printf("%-20s %-40s %s%s\n", clip(e.Name, 20), clip(e.URL, 40), on, state)
	}
	fmt.Printf("\nSubscribable events: %s\n", strings.Join(kinds, ", "))
}

func webhookAdd(args []string) {
	if len(args) < 2 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[1], "-") {
		fatal(fmt.Errorf("ptln webhook add needs a name and a URL:\n  ptln webhook add \"CI\" https://ci.example.com/partyline --on run.failed"))
	}
	name, url := args[0], args[1]
	rest := args[2:]

	var kinds []string
	if on := flagVal(rest, "--on"); on != "" {
		for _, k := range strings.Split(on, ",") {
			if k = strings.TrimSpace(k); k != "" {
				kinds = append(kinds, k)
			}
		}
		// Checked here as well as server-side so a typo fails before the endpoint exists, rather
		// than creating one that silently never fires.
		_, valid, err := settingsClient().ListWebhooks()
		if err == nil {
			for _, k := range kinds {
				if !advertises(valid, k) {
					fatal(fmt.Errorf("unknown event %q. Available: %s", k, strings.Join(valid, ", ")))
				}
			}
		}
	}

	id, secret, err := settingsClient().CreateWebhook(name, url, kinds)
	if err != nil {
		fatal(err)
	}

	if hasFlag(rest, "--key-only") {
		fmt.Fprintf(os.Stderr, "Added %s → %s\n", name, url)
		fmt.Println(secret) // stdout, alone, so it pipes
		return
	}
	if hasFlag(rest, "--json") {
		b, _ := json.MarshalIndent(map[string]any{"id": id, "name": name, "url": url, "kinds": kinds, "secret": secret}, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("Added %s → %s\n\nSigning secret (shown once — store it now):\n  %s\n\nYour receiver verifies each delivery with it. If you lose it, remove the endpoint and add it again.\n", name, url, secret)
}

func webhookRemove(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("ptln webhook rm needs a name or id (see: ptln webhook ls)"))
	}
	eps, _, err := settingsClient().ListWebhooks()
	if err != nil {
		fatal(err)
	}
	var found *api.Webhook
	for i, e := range eps {
		if e.ID == args[0] || strings.EqualFold(e.Name, args[0]) {
			found = &eps[i]
			break
		}
	}
	if found == nil {
		// Name what IS there. A dead-end error costs an agent a whole turn.
		names := make([]string, 0, len(eps))
		for _, e := range eps {
			names = append(names, e.Name)
		}
		if len(names) == 0 {
			fatal(fmt.Errorf("no webhook endpoints on this team"))
		}
		fatal(fmt.Errorf("no webhook called %q. Available: %s", args[0], strings.Join(names, ", ")))
	}
	if err := settingsClient().DeleteWebhook(found.ID); err != nil {
		fatal(err)
	}
	fmt.Printf("Removed %s. Deliveries to %s stop now.\n", found.Name, found.URL)
}
