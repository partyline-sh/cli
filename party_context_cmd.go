package main

import (
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// ptln party context — switch a live party's persona and/or project from the terminal.
//
// The HUMAN half of C2. The agent has had switch_context as an MCP tool since v0.37.0; without this
// a person could only do the same thing by hand-rolling an HTTP call. A capability an agent has and
// its human does not is exactly the asymmetry the four-part rule exists to catch.
//
// Same endpoint, same validation, same announcement into the transcript — the only difference is
// which token authenticates it.
func partyContext(args []string) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		partyContextUsage()
		if len(args) == 0 {
			os.Exit(2)
		}
		return
	}
	partyID := args[0]
	mode, label := "", ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--mode":
			if i+1 < len(args) {
				i++
				mode = args[i]
			}
		case "--project":
			if i+1 < len(args) {
				i++
				label = args[i]
			}
		default:
			fmt.Fprintf(os.Stderr, "ptln party context: unexpected %q\n\n", args[i])
			partyContextUsage()
			os.Exit(2)
		}
	}
	if mode == "" && label == "" {
		fmt.Fprintln(os.Stderr, "Nothing to change — pass --mode, --project, or both.")
		partyContextUsage()
		os.Exit(2)
	}

	out, err := api.New().SwitchPartyContext(partyID, strings.TrimSpace(mode), strings.TrimSpace(label))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptln party context: %v\n", err)
		os.Exit(1)
	}
	line := "✓ switched · persona: " + out.Mode
	if out.ProjectLabel != "" {
		line += " · project: " + out.ProjectLabel
	}
	fmt.Println(line)
	// Say WHEN it takes effect. A live agent is mid-turn under the old rules, and someone who
	// expects the very next reply to have changed will read a correct switch as a broken one.
	if out.Applies != "" {
		fmt.Printf("  applies %s — a turn already in flight finishes under the old persona.\n", out.Applies)
	}
	fmt.Println("  announced in the transcript, so everyone in the room sees which rules are in force.")
}

func partyContextUsage() {
	fmt.Fprintln(os.Stderr, "usage: ptln party context <party-id> [--mode <persona>] [--project <label>]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Re-point a LIVE party without losing the conversation.")
	fmt.Fprintln(os.Stderr, "  personas: chat · incident · prd · approach · fix · brainstorm · describe · project_setup")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  ptln party context 3f9a… --mode fix")
	fmt.Fprintln(os.Stderr, "  ptln party context 3f9a… --project checkout --mode describe")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  In a chat or a party, just say it — the agent switches itself (switch_context).")
}
