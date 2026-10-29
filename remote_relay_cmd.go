package main

import (
	"context"
	"fmt"
	"os"

	"partyline.sh/partyline/internal/clispec"
)

func relayMain(args []string) {
	if len(args) == 0 {
		clispec.MaybeHelp("relay", []string{"relay", "--help"})
		return
	}
	ctx := context.Background()
	var err error
	switch args[0] {
	case "deploy":
		domain := ""
		if len(args) == 3 && args[1] == "--domain" {
			domain = args[2]
		} else if len(args) != 1 {
			fatal(fmt.Errorf("usage: ptln relay deploy [--domain <host>]"))
		}
		err = relayDeploy(ctx, domain)
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
