// login / sessions subcommands — thin clients of the control plane.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/api"
)

func loginMain() {
	c := api.New()
	ds, err := c.DeviceStart()
	if err != nil {
		fatal(fmt.Errorf("can't reach %s — is the control plane up? (%w)", c.Base, err))
	}
	// Show the code prominently: the user must see it in BOTH the terminal and the
	// browser and confirm they match before approving — that's what stops someone
	// pasting a phished link to approve a login that isn't theirs.
	code := ds.UserCode
	fmt.Printf("\n☎ ptln login\n\n")
	fmt.Printf("    ┌─%s─┐\n", strings.Repeat("─", len(code)+10))
	fmt.Printf("    │  Your code:  %s  │\n", code)
	fmt.Printf("    └─%s─┘\n\n", strings.Repeat("─", len(code)+10))
	fmt.Printf("  1. We opened %s\n", ds.VerificationURI)
	fmt.Printf("     (not opening? paste that URL into your browser)\n")
	fmt.Printf("  2. Confirm the browser shows the SAME code above, then click Approve.\n\n")
	openBrowser(ds.VerificationURI)
	fmt.Print("  waiting for approval (ctrl-c to cancel) ")
	tok, err := c.DevicePoll(ds.DeviceCode, ds.Interval, ds.ExpiresIn)
	if err != nil {
		fmt.Println()
		fatal(err)
	}
	if err := api.SaveToken(tok); err != nil {
		fatal(err)
	}
	fmt.Println("  🔑 logged in — token saved to ~/.partyline/token")
}

func logoutMain() {
	if api.LoadToken() == "" {
		fmt.Println("not logged in")
		return
	}
	if err := api.ClearToken(); err != nil {
		fatal(err)
	}
	fmt.Println("👋 logged out — token removed from ~/.partyline/token")
}

func whoamiMain() {
	c := api.New()
	if c.Token == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login`"))
	}
	p, err := c.Me()
	if err != nil {
		fatal(err)
	}
	// Lead with the display name — that's how teammates see you. The handle is a
	// unique id (auto-generated), not your username; set your name in Settings → Profile.
	name := p.DisplayName
	if name == "" {
		name = "(not set — Settings → Profile)"
	}
	fmt.Printf("name:    %s\n", name)
	fmt.Printf("email:   %s\n", p.Email)
	if p.GithubUsername != "" {
		fmt.Printf("github:  %s\n", p.GithubUsername)
	}
	if p.Timezone != "" {
		fmt.Printf("tz:      %s\n", p.Timezone)
	}
	fmt.Printf("id:      %s\n", p.Handle) // unique id, not your display name
}

func sessionsMain() {
	c := api.New()
	if c.Token == "" {
		fatal(fmt.Errorf("not logged in — run `ptln login`"))
	}
	ss, err := c.ListSessions()
	if err != nil {
		fatal(err)
	}
	if len(ss) == 0 {
		fmt.Println("no live sessions")
		return
	}
	for _, s := range ss {
		fmt.Printf("● %-22s since %s\n", s.JoinCode, s.StartedAt)
	}
}

// joinPick lists the live sessions you can join (ones you're invited to / can see
// but don't host) and joins the one you choose. This is `ptln join` with no
// link — the authenticated "I was invited, let me in" path.
func joinPick() {
	c := api.New()
	if c.Token == "" {
		fatal(fmt.Errorf("not logged in.\n  Join with a link:   ptln join <link>\n  Or log in to see your invites:   ptln login"))
	}
	ss, err := c.ListSessions()
	if err != nil {
		fatal(err)
	}
	var joinable []api.SessionInfo
	for _, s := range ss {
		if s.Status == "live" && s.JoinLink != "" && !s.IsHost {
			joinable = append(joinable, s)
		}
	}
	if len(joinable) == 0 {
		fmt.Println("☎ no live sessions to join right now.")
		fmt.Println("  Sessions you're invited to show up here. To join by link:")
		fmt.Println("    ptln join <link>")
		return
	}
	fmt.Println("☎ sessions you can join:")
	for i, s := range joinable {
		fmt.Printf("  %d) %s\n", i+1, s.JoinCode)
	}
	fmt.Print("join which number? (enter to cancel): ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(joinable) {
		fatal(fmt.Errorf("not a valid choice"))
	}
	joinMain([]string{joinable[n-1].JoinLink}) // reuse the link-join path
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "linux":
		_ = exec.Command("xdg-open", url).Start()
	}
}

// relayName is the identity we present on the relay path. Sanitized to the
// username charset (no ".", which the relay uses to split code from name).
func relayName() string {
	n := os.Getenv("PARTYLINE_NAME")
	if n == "" {
		n = defaultName()
	}
	var b strings.Builder
	for _, r := range n {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "guest"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

func joinMain(args []string) {
	if len(args) < 1 {
		// No link given: if you're logged in, show the sessions you've been
		// invited to and let you pick one. Otherwise explain the link form.
		joinPick()
		return
	}
	code, key, relay, err := parseJoinLink(args[0])
	if err != nil {
		fatal(err)
	}
	// Relay resolution order: the relay baked into the link (control-plane-assigned
	// at session creation) wins; then a PARTYLINE_RELAY override; then the legacy
	// default. This is what lets us grow the relay pool without stranding installs.
	relayAddr := relay
	if relayAddr == "" {
		relayAddr = os.Getenv("PARTYLINE_RELAY")
	}
	if relayAddr == "" {
		relayAddr = "pppp.sh:22"
	}
	// If logged in, prove our partyline identity to the host: fetch a signed
	// assertion for this code and send it instead of a plain name. Not logged in
	// (or not authorized) → join anonymously with a self-asserted name (view-only).
	// Re-minted on every (re)connect: the assertion is short-lived, so a fresh one
	// is fetched on reconnect rather than reusing an expired one on an invite-only
	// line. Falls back to the plain relay name if minting fails / not logged in.
	c := api.New()
	identFn := func() string {
		if c.Token != "" {
			if a, err := c.SessionIdentity(code); err == nil && a != "" {
				return a
			}
		}
		return relayName()
	}
	if err := joinE2EE(relayAddr, code, key, identFn); err != nil {
		fatal(err)
	}
}
