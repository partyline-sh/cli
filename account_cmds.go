// login / sessions subcommands — thin clients of the control plane.
package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/identity"
)

func loginMain(args []string) {
	// `ptln login [<url>] [--accept-new-key]`. The URL names the instance to authenticate against —
	// the self-host path — and is also where this machine's identity TRUST ROOT comes from (H.2).
	acceptNewKey := false
	instance := ""
	for _, a := range args {
		switch {
		case a == "--accept-new-key":
			// Deliberately a flag you type, never an env var or a config field: it exists to be
			// used once, by a person who has just READ a fingerprint in a refusal message and
			// checked it on the server. A default-on switch would delete the pin's whole point.
			acceptNewKey = true
		case strings.HasPrefix(a, "-"):
			fatal(fmt.Errorf("ptln login: unknown flag %q (usage: ptln login [<url>] [--accept-new-key])", a))
		case instance == "":
			instance = strings.TrimRight(strings.TrimSpace(a), "/")
		default:
			fatal(fmt.Errorf("ptln login: unexpected argument %q (usage: ptln login [<url>] [--accept-new-key])", a))
		}
	}
	// `ptln login` with no URL used to mean "log in to partyline.sh". There is no hosted service
	// any more. But on a box that HOSTS an install, bare login has one obvious meaning — the
	// instance running right here — and telling that operator to go find their own URL was a
	// wall (observed: an operator stood on the hosting box, ran `ptln login`, got a lecture).
	// The install's recorded directory names the site; use it, saying so.
	if instance == "" && api.Unconfigured() {
		if site := localInstanceSite(); site != "" {
			instance = site
			fmt.Printf("☎ this box hosts a partyline instance — logging in to it\n")
		} else if found := discoverInstances(); len(found) > 0 {
			// Epic PA · P2: the network names the instance so nobody types a URL. Picking one
			// is still just picking — the device flow decides who gets in.
			instance = pickDiscoveredInstance(found)
			if instance == "" {
				fatal(fmt.Errorf("ptln login: cancelled"))
			}
		} else {
			fatal(fmt.Errorf("ptln login: %s", api.NoInstanceNotice()))
		}
	}
	if instance != "" {
		if err := validInstanceURL(instance); err != nil {
			fatal(fmt.Errorf("ptln login %s: %w", instance, err))
		}
		// Point every path in this process — API base, token path, config dir, trust pin — at that
		// instance. api.ConfigDir() is per-endpoint, so this cannot overwrite a production login.
		if err := os.Setenv("PARTYLINE_API", instance); err != nil {
			fatal(err)
		}
		fmt.Printf("☎ instance: %s\n", instance)
		fmt.Printf("  (for every OTHER command against it in this shell: export PARTYLINE_API=%s)\n", instance)
	}

	c := api.New()
	ds, err := c.DeviceStart()
	if err != nil && api.IsUnknownAuthority(err) {
		// A self-hosted instance on its own CA (Caddy internal, the self-host default). The
		// SSH answer: show the fingerprint once, pin on an explicit yes, verify against the
		// pin forever after. A later certificate swap fails loudly again.
		if trustInstanceCert(c.Base) {
			c = api.New() // a fresh client picks up the pin
			ds, err = c.DeviceStart()
		}
	}
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
	// Cache the identity so the session manager can show "logged in as …" without a
	// network call — and so it's obvious WHICH account a daemon enabled here binds to.
	c.Token = tok
	who := ""
	if p, err := c.Me(); err == nil {
		_ = api.SaveAccount(api.Account{Email: p.Email, Name: p.DisplayName})
		who = " as " + p.Email
	}
	// The path is per-control-plane (api.ConfigDir): prod uses ~/.partyline, staging and local get
	// their own dir so dogfooding cannot overwrite a prod login. Hardcoding the prod path told a
	// staging login it had just written the prod token — alarming, and wrong.
	fmt.Printf("  🔑 logged in%s — token saved to %s\n", who, api.TokenPath())
	// If a team invited this address, join it now. Terminal-first people should never have to
	// find an email to end up on their team — and the invite may have been sent AFTER they
	// signed up, which no signup-time hook can catch. Best-effort: a failure here must never
	// make a successful login look failed.
	if n, team, err := c.ClaimInvites(); err == nil && n > 0 {
		if team != "" {
			fmt.Printf("  ☎ joined %s — you had an invitation waiting\n", team)
		} else {
			fmt.Printf("  ☎ joined %d team(s) — you had an invitation waiting\n", n)
		}
	}
	// Pin the instance's identity trust root (H.2). This is the key the HOST of a shared terminal
	// uses to decide that a joiner really is who the control plane says they are, so it is fetched
	// over TLS, pinned, and its fingerprint printed. A CHANGED key stops here — see trust.go.
	pinTrustRoot(c, acceptNewKey)
	// Login IS setup: the checklist runs after every successful login (setup.go). Configured
	// steps render ✓ and ask nothing; gaps ask one question each; Enter keeps previous answers.
	// Local-only users who never log in never see it.
	runSetupAfterLogin()
}

// validInstanceURL keeps `ptln login <url>` on a leg TLS actually authenticates. TOFU here is only
// safe because a certificate for that domain is required to answer — over plain http anyone on the
// path can hand you their own key. Loopback is exempt: it is how you test an instance you are
// building, and there is no network to sit on.
func validInstanceURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("not a URL — expected something like https://ptln.example.com")
	}
	host := u.Hostname()
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if loopback {
			return nil
		}
		return fmt.Errorf("refusing http:// — the identity key is fetched over this connection and only TLS authenticates the instance.\n  Use https://%s (http:// is allowed for localhost only)", u.Host)
	default:
		return fmt.Errorf("unsupported scheme %q — use https://", u.Scheme)
	}
}

// pinTrustRoot performs the TOFU step of `ptln login`: fetch this instance's assertion public key,
// check it against whatever is already pinned, and pin it. Never silent — the fingerprint is
// printed either way, so support has something concrete to ask for and anyone who cares can
// compare it with `ptln server doctor` run on the box itself.
func pinTrustRoot(c *api.Client, acceptNewKey bool) {
	key, _, ok, err := c.AssertKey()
	if err != nil || !ok {
		// An instance that publishes no key is a normal state (older build, or identity signing
		// simply not configured). Leave any existing pin ALONE: dropping it on a failed fetch
		// would let an instance disown its own key to get back to the compiled-in default.
		if fp, source, _, terr := identity.TrustRoot(); terr == nil {
			fmt.Printf("  🔒 identity trust root unchanged: %s (%s)\n", fp, source)
		}
		return
	}
	if cerr := identity.CheckOfferedKey(key); cerr != nil {
		if !acceptNewKey {
			// No continue-anyway path: the pin exists to DETECT substitution, and a detection that
			// proceeds anyway detects nothing.
			fatal(cerr)
		}
		fmt.Printf("⚠ accepting a CHANGED identity key because --accept-new-key was given.\n")
	}
	if err := identity.SavePin(api.Base(), key); err != nil {
		fatal(fmt.Errorf("could not pin this instance's identity key: %w", err))
	}
	fp, _ := identity.FingerprintB64(key)
	fmt.Printf("  🔒 identity trust root pinned: %s\n", fp)
	fmt.Printf("     compare it with `ptln server doctor` on that instance; re-pin with `ptln login <url> --accept-new-key`\n")
}

func logoutMain() {
	if api.LoadToken() == "" {
		fmt.Println("not logged in")
		return
	}
	if err := api.ClearToken(); err != nil {
		fatal(err)
	}
	_ = api.ClearAccount() // drop the cached identity too
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
	_ = api.SaveAccount(api.Account{Email: p.Email, Name: p.DisplayName}) // keep the UI cache fresh
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
	// A typo used to `fatal` here; Pick just cancels, so a slip costs you nothing.
	n, ok := Pick("join which number", joinable, func(s api.SessionInfo) string { return s.JoinCode })
	if !ok {
		return
	}
	joinMain([]string{joinable[n].JoinLink}) // reuse the link-join path
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
	// No default host: pppp.sh is retired, and guessing one means dialling something that cannot
	// answer. A join link carries its relay in the fragment (#r=), so the ordinary path never
	// reaches here empty.
	// If logged in, prove our partyline identity to the host: fetch a signed
	// assertion for this code and send it instead of a plain name. Not logged in
	// (or not authorized) → join anonymously with a self-asserted name (view-only).
	// Re-minted on every (re)connect: the assertion is short-lived, so a fresh one
	// is fetched on reconnect rather than reusing an expired one on an invite-only
	// line. Falls back to the plain relay name if minting fails / not logged in.
	c := api.New()
	// SAY WHY when a logged-in joiner can't prove who they are. Silently downgrading to an
	// anonymous name sends you to an invite-only host that refuses you with "you're not signed
	// in" — while `ptln whoami` insists you are. The reason (control plane can't sign, session
	// not visible to you, expired link) is right here and is the only thing that explains it.
	// Warn once: identFn re-mints on every reconnect and a repeated warning would be noise.
	warned := false
	identFn := func() string {
		if c.Token != "" {
			a, err := c.SessionIdentity(code)
			if err == nil && a != "" {
				return a
			}
			if !warned {
				warned = true
				reason := "no assertion returned"
				if err != nil {
					reason = err.Error()
				}
				fmt.Printf("⚠ couldn't prove your partyline identity for this session: %s\r\n"+
					"  joining anonymously — an invite-only host will refuse this.\r\n", reason)
			}
		}
		return relayName()
	}
	if err := joinE2EE(relayAddr, code, key, identFn); err != nil {
		fatal(err)
	}
}
