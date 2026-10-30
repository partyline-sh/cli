package main

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/term"
	"partyline.sh/partyline/internal/clispec"
)

func relayMain(args []string) {
	if len(args) == 0 {
		// In a terminal, the settings screen; elsewhere (a pipe, an agent's shell), the help.
		if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
			if err := relaySettings(); err != nil {
				fatal(err)
			}
			return
		}
		clispec.MaybeHelp("relay", []string{"relay", "--help"})
		return
	}
	ctx := context.Background()
	var err error
	switch args[0] {
	case "deploy":
		if len(args) != 1 {
			fatal(fmt.Errorf("usage: ptln relay deploy"))
		}
		err = relayDeploy(ctx)
	case "forget-token":
		err = relayForgetToken()
	case "invite":
		if len(args) != 1 {
			fatal(fmt.Errorf("usage: ptln relay invite"))
		}
		var invite string
		invite, err = relayInvite(ctx)
		if err == nil {
			fmt.Println(invite)
		}
	case "join":
		if len(args) != 2 {
			fatal(fmt.Errorf("usage: ptln relay join <string>"))
		}
		err = relayJoin(ctx, args[1])
		if err == nil {
			ensureWatcher()
			fmt.Println("Joined relay.")
		}
	case "status":
		if len(args) != 1 {
			fatal(fmt.Errorf("usage: ptln relay status"))
		}
		var c relayConfig
		var token string
		c, token, err = loadRelay()
		if err == nil {
			fmt.Fprintf(os.Stdout, "Relay: %s\nRP ID: %s\n", c.URL, c.RPID)
			if assetErr := relayCheckAssets(ctx, c); assetErr != nil {
				fmt.Printf("App assets: %s\n", assetErr)
			} else {
				fmt.Println("App assets: match")
			}
			if relayOutdated(c) {
				fmt.Println("App version: older than this partyline; `ptln relay deploy` updates it")
			} else {
				fmt.Println("App version: current")
			}
			fmt.Println("Automatic updates: " + relayAutoLine(c))
			var result struct {
				Connected bool `json:"connected"`
			}
			err = relayPost(ctx, c.URL, "/status", map[string]string{"id": c.MachineID, "token": token}, &result)
			if err == nil {
				fmt.Printf("Connected: %t\n", result.Connected)
			} else {
				fmt.Println("Connected: unknown (relay unreachable)")
			}
		}
	default:
		err = fmt.Errorf("unknown relay command %q", args[0])
	}
	if err != nil {
		fatal(err)
	}
}
