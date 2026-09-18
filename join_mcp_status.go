package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/clispec"
)

// ptln join-mcp status — the outside view of every party MCP registration on this machine, and
// whether each one's party is still going.
//
// A registration is permanent; a party is not. Every `ptln join-mcp` leaves an entry in an
// engine's config, and once the party closes that entry is a server that starts, connects to
// nothing useful and quietly costs a slot in every session that engine opens from then on. There
// was no way to see the set — hence this: machine-wide by default and GROUPED BY THE FILE each
// entry came from, because the accumulation is across parties, not within one repo.
//
// Three properties this command promises:
//
//   - It never prints a party token, in either output form. The token is an unexported field
//     (see party_registrations.go), so this is structural rather than a rule to remember.
//   - It writes NOTHING. Every file it touches it opens read-only; removal is a command it
//     PRINTS for a human to run, in that human's own config file.
//   - "Uncheckable" is its own answer. A party we could not reach is never reported as ended,
//     and never sets the failing exit code — telling someone on a plane that their party ended
//     is the same lie as telling them nothing is wrong, in a more confident voice.
//
// Exit code: 1 if any party has ENDED (so `ptln join-mcp status || …` composes in a script),
// 0 when everything is live, uncheckable, or absent.
func joinMCPStatusMain(args []string) {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--help", "-h", "help":
			joinMCPUsage(os.Stdout)
			return
		default:
			fmt.Fprintf(os.Stderr, "ptln join-mcp status: unknown flag %q (flags: --json)\n", a)
			os.Exit(2)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln join-mcp status: cannot find your home directory: %v\n", err)
		os.Exit(2)
	}
	dir, _ := os.Getwd()
	regs := scanPartyRegistrations(home, dir)
	probePartyRegistrations(regs)

	if !renderJoinMCPStatus(os.Stdout, regs, asJSON) {
		os.Exit(1)
	}
}

// probePartyRegistrations fills in each registration's status, reusing the three-state party
// liveness probe (internal/api). Concurrent because the answer for one party says nothing about
// another and a stale host should not make the whole listing wait its timeout out in series.
func probePartyRegistrations(regs []partyRegistration) {
	var wg sync.WaitGroup
	for i := range regs {
		r := &regs[i]
		if r.Base == "" || r.PartyID == "" || r.token == "" {
			continue // nothing to probe with — stays uncheckable, which is the truth
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &api.PartyClient{Base: r.Base, ID: r.PartyID, Token: r.token}
			switch c.ProbeLiveness() {
			case api.PartyLive:
				r.Status = statusLive
			case api.PartyEnded:
				r.Status = statusEnded
			default:
				r.Status = statusUncheckable
			}
		}()
	}
	wg.Wait()
}

// renderJoinMCPStatus writes the report and reports whether the machine is CLEAN — no ended
// party. Takes a writer and the already-probed registrations so the whole rendering, including
// the "no token anywhere in the output" property, is testable without a network or a home dir.
func renderJoinMCPStatus(w io.Writer, regs []partyRegistration, asJSON bool) bool {
	ended := 0
	counts := map[string]int{}
	for _, r := range regs {
		counts[r.Status]++
		if r.Status == statusEnded {
			ended++
		}
	}

	if asJSON {
		// Never nil: a machine with no registrations still emits valid, iterable output.
		out := struct {
			Registrations []partyRegistration `json:"registrations"`
			Live          int                 `json:"live"`
			Ended         int                 `json:"ended"`
			Uncheckable   int                 `json:"uncheckable"`
		}{append([]partyRegistration{}, regs...), counts[statusLive], counts[statusEnded], counts[statusUncheckable]}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil { // unreachable for these types, but never print a half-written report
			fmt.Fprintln(w, "{}")
			return ended == 0
		}
		fmt.Fprintln(w, string(b))
		return ended == 0
	}

	fmt.Fprintln(w, "party MCP registrations on this machine")
	if len(regs) == 0 {
		fmt.Fprintln(w, "\n  none — nothing on this machine is wired to a party.")
		fmt.Fprintln(w, "  Join one with: ptln join-mcp '<party link>'")
		return true
	}

	source := ""
	for _, r := range regs {
		if r.Source != source {
			source = r.Source
			fmt.Fprintf(w, "\n%s\n", source)
		}
		fmt.Fprintf(w, "  %s %-11s %s  party %s  @%s\n", statusMark(r.Status), r.Status, r.Server, r.PartyID, r.AgentName)
		fmt.Fprintf(w, "      scope: %s\n", r.Scope)
		if r.Status == statusEnded {
			fmt.Fprintf(w, "      this party has ended — remove it: %s\n", r.Remove)
		}
	}
	fmt.Fprintf(w, "\n%d live · %d ended · %d uncheckable\n", counts[statusLive], counts[statusEnded], counts[statusUncheckable])
	if counts[statusUncheckable] > 0 {
		// Said explicitly, because the one thing this report must not do is let a run with no
		// network read as "everything is fine" OR as "these parties are dead".
		fmt.Fprintln(w, "Uncheckable means we could not reach the party's control plane — not that it ended.")
	}
	fmt.Fprintln(w, "\nNothing here was modified. Removal is yours to run.")
	return ended == 0
}

func statusMark(status string) string {
	switch status {
	case statusLive:
		return "✓"
	case statusEnded:
		return "✗"
	default:
		return "?"
	}
}

// joinMCPUsage prints join-mcp's help from the one declaration `ptln join-mcp help` renders,
// so the subcommand and the command cannot describe themselves differently.
func joinMCPUsage(w io.Writer) {
	spec, _ := clispec.Lookup("join-mcp")
	clispec.PrintHelp(w, spec)
}
