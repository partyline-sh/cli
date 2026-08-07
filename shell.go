// Shared-shell mode: the default partyline experience. Your terminal goes
// raw-passthrough to a pty running your shell; joiners mirror it over ssh.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/obs"
	"partyline.sh/partyline/internal/ptysess"
	"partyline.sh/partyline/internal/relay"
	"partyline.sh/partyline/internal/sshd"
	"partyline.sh/partyline/internal/wormhole"
)

func shellMain() {
	// Refuse to nest. Starting a partyline session inside a partyline session
	// spawns a shell whose terminal queries (cursor-position `\x1b[6n`, color
	// reports) have no real terminal to answer them, so the inner shell stalls —
	// this is the "second `partyline` hangs" bug. We tag every child shell with
	// PARTYLINE=1 (see ptysess.New) precisely so we can catch this here.
	if os.Getenv("PARTYLINE") == "1" {
		fmt.Fprintln(os.Stderr, "☎ you're already in a partyline session.")
		fmt.Fprintln(os.Stderr, "  Open a separate terminal to host another — or run /pexit to leave this one first.")
		os.Exit(1)
	}

	// Update awareness: surface a cached "newer version" notice (install-aware,
	// to stderr) and kick a throttled background refresh. The host runs long, so
	// the refresh completes and the notice shows on the next session start.
	notifyIfBehind(os.Stderr)
	maybeRefreshUpdateCacheAsync()

	fs := flag.NewFlagSet("partyline", flag.ExitOnError)
	port := fs.Int("port", 2222, "ssh port joiners connect to")
	name := fs.String("name", "", "host display name (default: your profile display name)")
	open := fs.Bool("open", false, "guests can type immediately (default: view-only until granted)")
	invite := fs.String("invite", "", "comma-separated emails to invite when the session opens")
	allow := fs.String("allow", "", "comma-separated GitHub usernames allowed to join (default: any verified GitHub user)")
	insecure := fs.Bool("insecure-any-key", false, "accept ANY ssh key without identity verification (trusted offline/LAN only)")
	relayAddr := fs.String("relay", "pppp.sh:22", "relay host:port for ssh <code>@<relay> joining ('' to disable)")
	relayCode := fs.String("relay-code", "", "explicit relay join code (default: control-plane join code)")
	claimID := fs.String("session", "", "claim a planned session created from the web UI (goes live, fires its invites)")
	inviteOnly := fs.Bool("invite-only", true, "require a partyline account to join (default; --invite-only=false allows anonymous view-only)")
	team := fs.String("team", "", "host this session for a team (slug); default: your personal space")
	announce := fs.Bool("announce", false, "post the join code to your team's connected Slack channel (needs --team with Slack connected)")

	args := os.Args[1:]
	var cmdv []string
	for i, a := range args {
		if a == "--" {
			cmdv = args[i+1:]
			args = args[:i]
			break
		}
	}
	_ = fs.Parse(args)
	if len(cmdv) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		cmdv = []string{sh}
	}

	// Resolve ONCE and reuse for Attach below. The session and the host's own roster entry
	// must be the same name — passing the raw `*name` to Attach (empty unless --name was
	// given) made uniqueNameLocked fall back to "guest", so /pwho listed the signed-in host
	// as `guest(host)` while every joiner showed their real verified handle.
	hostName := resolveHostName(*name)
	sess, err := ptysess.New(cmdv, hostName, *open)
	if err != nil {
		fatal(err)
	}

	var allowList []string
	for _, a := range strings.Split(*allow, ",") {
		if a = strings.TrimSpace(a); a != "" {
			allowList = append(allowList, a)
		}
	}
	fmt.Printf("☎ partyline — shared shell up: %s\n", strings.Join(cmdv, " "))

	srv, err := sshd.StartShell(sess, fmt.Sprintf(":%d", *port), filepath.Join(stateDir(), "host_key"),
		sshd.Options{InsecureAnyKey: *insecure, Allow: allowList})
	if err != nil {
		// Local SSH (direct/tailnet joins) is OPTIONAL — the relay is the universal
		// join path. A bind failure almost always means another partyline already
		// holds the port; warn and keep going instead of exiting (the old bug:
		// fatal() here made `partyline` "open and immediately close").
		fmt.Printf("   ⚠ local SSH on :%d unavailable (%v) — relay joins still work\n", *port, err)
		srv = nil
	}
	if srv != nil {
		defer func() { _ = srv.Close() }()
	}

	fmt.Printf("   quit: /pexit  (or ctrl-\\ then q) — Ctrl-C interrupts your program, it won't quit\n")
	fmt.Printf("   commands: ctrl-\\ ? for the menu · /pwho · /phud [on|off] · /pgrant [name] · /pinvite <email> · /phelp\n")

	// control-plane registration: optional sugar, offline-first — a missing token
	// or unreachable API never blocks the line.
	var sessionID, joinCode string
	useRelay := *relayAddr // the control plane may assign a pool relay on register (below)
	apic := api.New()
	// Per-session E2EE key. NOTE: the key IS sent to the control plane on register
	// (so the web app / invites can render the full join link) — the relay stays
	// blind (ciphertext only), but partyline could decrypt if it tapped the relay.
	// Honest claim: "encrypted in transit; the relay can't read it", NOT zero-knowledge.
	// See docs/reviews/0004 (CR4) and migration 0008.
	sessionKey := make([]byte, wormhole.KeyLen)
	// crypto/rand.Read returns n==len(b) iff err==nil; a failure must abort, never
	// fall through to an all-zero (fully predictable) key.
	if _, err := rand.Read(sessionKey); err != nil {
		fmt.Fprintln(os.Stderr, "ptln: failed to generate session key:", err)
		os.Exit(1)
	}
	keyB64 := base64.RawURLEncoding.EncodeToString(sessionKey)
	if apic.Token != "" {
		var eps []api.Endpoint
		if *relayAddr != "" { // relay first — the universal, code-auth join path
			rh, rp, _ := strings.Cut(*relayAddr, ":")
			rport := 22
			if rp != "" {
				fmt.Sscanf(rp, "%d", &rport)
			}
			eps = append(eps, api.Endpoint{Type: "relay", Addr: rh, Port: rport})
		}
		for _, ip := range localIPs() {
			eps = append(eps, api.Endpoint{Type: "tailnet", Addr: ip, Port: *port})
		}
		// --session claims a planned line from the web UI; otherwise register a fresh one.
		var reg *api.RegisterOut
		if *claimID != "" {
			reg, err = apic.ClaimSession(*claimID, eps, keyB64)
		} else {
			reg, err = apic.RegisterSession(eps, "org", *team, keyB64, *announce)
		}
		if err == nil {
			sessionID = reg.ID
			joinCode = reg.JoinCode
			if reg.Relay != "" { // control-plane-assigned relay (pool director, see docs/SCALING.md)
				useRelay = reg.Relay
			}
			// The relay travels in the link fragment (with the key, never sent to a
			// server) so anonymous joiners dial the right relay without a lookup —
			// and adding relays later never strands existing installs.
			link := fmt.Sprintf("https://partyline.sh/j/%s#k=%s&r=%s", reg.JoinCode, keyB64, useRelay)
			fmt.Printf("   ☎ end-to-end encrypted — share this link:\n     %s\n", link)
			fmt.Printf("   joiners run:  ptln join '%s'\n", link)
			sess.OnInvite = func(target string) string {
				// @teamname → invite that team's members; else treat as an email.
				if strings.HasPrefix(target, "@") {
					want := strings.ToLower(strings.TrimPrefix(target, "@"))
					orgs, err := apic.ListOrgs()
					if err != nil {
						return "invite to " + target + " failed: " + err.Error()
					}
					for _, o := range orgs {
						if o.Personal {
							continue
						}
						if strings.ToLower(o.Slug) == want || strings.ToLower(o.Name) == want {
							ms, err := apic.OrgMembers(o.Slug)
							if err != nil {
								return "invite to " + target + " failed: " + err.Error()
							}
							var uids []string
							for _, m := range ms {
								uids = append(uids, m.UserID)
							}
							if n, err := apic.InviteTargets(sessionID, map[string]any{"users": uids}); err != nil {
								return "invite to " + target + " failed: " + err.Error()
							} else {
								return fmt.Sprintf("☎ invited team %s (%d members)", o.Name, n)
							}
						}
					}
					return "no team named " + target
				}
				if _, err := apic.InviteSession(sessionID, []string{target}); err != nil {
					return "invite to " + target + " failed: " + err.Error()
				}
				return "☎ invited " + target
			}
			if *invite != "" {
				var emails []string
				for _, e := range strings.Split(*invite, ",") {
					if e = strings.TrimSpace(e); e != "" {
						emails = append(emails, e)
					}
				}
				if n, err := apic.InviteSession(sessionID, emails); err == nil {
					fmt.Printf("   ✉️  invites sent: %d\n", n)
				} else {
					fmt.Printf("   (invites failed: %v)\n", err)
				}
			}
			go func() {
				defer obs.Guard("heartbeat")
				t := time.NewTicker(20 * time.Second)
				defer t.Stop()
				for range t.C {
					// If the session was ended remotely (killed from the web),
					// stop the local shell so the normal teardown runs.
					if apic.Heartbeat(sessionID, sess.Names()) {
						fmt.Print("\r\n☎ this session was ended from the web\r\n")
						sess.End() // kill the shell; the Done teardown runs the normal close
						return
					}
				}
			}()
		} else {
			fmt.Printf("   (session not registered: %v)\n", err)
		}
	}

	// relay: serve joiners over the blind, end-to-end-encrypted relay. The relay
	// verifies (via control plane) that we own the code, then only splices
	// ciphertext — it holds no key. Needs login + a registered line. Offline-first:
	// relay failure never blocks the local line.
	if useRelay != "" {
		rc := *relayCode
		if rc == "" {
			rc = joinCode
		}
		if rc == "" || apic.Token == "" {
			fmt.Printf("   (log in to share — `ptln login`)\n")
		} else {
			// Dial + register synchronously so the user sees real connect status;
			// then serve joiners in the background. A relay failure never blocks the
			// local shell — you can still work; others just can't join over the relay.
			fmt.Printf("   relay: connecting to %s …\n", useRelay)
			if ms, derr := relay.DialHostE2EE(useRelay, rc, apic.Token); derr != nil {
				fmt.Printf("   relay: ✗ %v — others can't join over the relay\n", derr)
			} else {
				fmt.Printf("   relay: ✓ connected — your link is live\n")
				// Serve joiners, and transparently re-dial if the relay connection
				// drops — a wifi blip or relay restart no longer permanently kills the
				// link. Same session + key, so joiners reconnect with their existing link.
				go relay.ServeHostReconnecting(sess, ms, sessionKey, rc, useRelay, apic.Token, *inviteOnly)
			}
		}
	}
	fmt.Println()

	// graceful kills end the line; SIGKILL is the reaper's job
	restoreTerm := func() {}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		if sessionID != "" {
			apic.EndSession(sessionID, sess.DriverCount())
		}
		restoreTerm()
		os.Exit(130)
	}()

	stdinFd := int(os.Stdin.Fd())
	interactive := term.IsTerminal(stdinFd)
	var host *ptysess.Participant
	if interactive {
		old, err := term.MakeRaw(stdinFd)
		if err != nil {
			fatal(err)
		}
		restoreTerm = func() { _ = term.Restore(stdinFd, old) }
		defer func() { _ = term.Restore(stdinFd, old) }()
		w, h, _ := term.GetSize(stdinFd)
		host = sess.Attach(hostName, os.Stdout, w, h, true, true)

		wch := make(chan os.Signal, 1)
		signal.Notify(wch, syscall.SIGWINCH)
		go func() {
			for range wch {
				if w, h, err := term.GetSize(stdinFd); err == nil {
					sess.Resize(host, w, h)
				}
			}
		}()
	} else {
		// no tty (tests/pipes): still host the line, line-buffered passthrough
		host = sess.Attach(hostName, os.Stdout, 80, 24, true, true)
	}

	if sessionID != "" {
		sess.OnPresence = func() { apic.Heartbeat(sessionID, sess.Names()) }
		go apic.Heartbeat(sessionID, sess.Names())
	}

	go func() {
		defer obs.Guard("stdin")
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if !sess.HandleInput(host, buf[:n]) {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	<-sess.Done
	if sessionID != "" {
		apic.EndSession(sessionID, sess.DriverCount())
	}
	sess.Detach(host)
	fmt.Print("\r\n☎ partyline session closed\r\n")
}
