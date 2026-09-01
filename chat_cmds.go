package main

import (
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// ptln chat — connect a chat account (Telegram, Discord) to this partyline account, so you can reach
// your projects from the app you already have open (docs/epics/chat-transports.md).
//
// THE FOUR-PART RULE: every feature needs an API endpoint, a CLI command that calls it, a line in
// `ptln settings`, and docs. This is part two — and it matters more than usual here, because the
// alternative is telling someone to open a browser to get a code they then type into a phone.

func chatCmd(args []string) {
	if len(args) == 0 {
		chatStatus()
		return
	}
	switch args[0] {
	case "link":
		chatLink(args[1:])
	case "unlink":
		chatUnlink(args[1:])
	case "status", "list":
		chatStatus()
	default:
		fmt.Fprintf(os.Stderr, "ptln chat: unknown subcommand %q\n\n", args[0])
		chatUsage()
		os.Exit(2)
	}
}

func chatUsage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  ptln chat                    what's connected")
	fmt.Fprintln(os.Stderr, "  ptln chat link telegram      get a one-time code to send to the bot")
	fmt.Fprintln(os.Stderr, "  ptln chat link discord")
	fmt.Fprintln(os.Stderr, "  ptln chat unlink telegram    disconnect")
}

func chatStatus() {
	res, err := api.New().ChatLinked()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln chat: %v\n", err)
		os.Exit(1)
	}
	if len(res.Linked) == 0 {
		// A teaching empty state: zero output should show the next step, not just say "none".
		fmt.Println("No chat accounts connected.")
		fmt.Println()
		fmt.Println("  ptln chat link telegram    then send the code to your partyline bot")
		fmt.Println("  ptln chat link discord     then /link code:… in a channel the bot is in")
		return
	}
	fmt.Println("Connected chat accounts:")
	for _, l := range res.Linked {
		name := l.DisplayName
		if name == "" {
			name = "(no display name)"
		}
		fmt.Printf("  %-10s %s\n", l.Platform, name)
	}
	fmt.Println()
	fmt.Println("Disconnect with: ptln chat unlink <platform>")
}

func chatLink(args []string) {
	if len(args) == 0 {
		chatUsage()
		os.Exit(2)
	}
	platform := strings.ToLower(strings.TrimSpace(args[0]))
	res, err := api.New().ChatLinkCode(platform)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln chat: %v\n", err)
		os.Exit(1)
	}

	// The code is a CREDENTIAL: redeeming it binds a chat account to this partyline account, with
	// this account's org permissions. It is shown once and never readable again — so print it
	// plainly, say what it does, and say how long it lasts.
	fmt.Printf("Send this to %s within %d minutes:\n\n", res.Where, res.ExpiresInMinutes)
	fmt.Printf("    %s\n\n", res.Send)
	fmt.Println("It works once. Anyone who has it can connect their chat account to YOUR partyline")
	fmt.Println("account — don't paste it into a shared channel. Lost it? Run this again.")
}

func chatUnlink(args []string) {
	if len(args) == 0 {
		chatUsage()
		os.Exit(2)
	}
	platform := strings.ToLower(strings.TrimSpace(args[0]))
	if err := api.New().ChatUnlink(platform); err != nil {
		fmt.Fprintf(os.Stderr, "ptln chat: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Disconnected %s. Any unused link codes for it were revoked too.\n", platform)
}
