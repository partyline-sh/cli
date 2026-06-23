package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
)

// joinMCPMain registers the partyline party MCP server into your OWN already-running LLM
// session, from a join link — so the session you're coding in can read and post to a party
// (the "two developers' sessions talk directly" use case). It's the human-friendly wrapper
// around the env wiring the runner does automatically for agents it spawns.
//
//	ptln join-mcp 'https://partyline.sh/p/<id>#t=<token>' [--name you] [--print]
//
// By default it runs `claude mcp add` for you; --print just shows the config (for codex /
// gemini / manual). The party token rides in the MCP server's env config (party-scoped and
// revocable — not your login token).
func joinMCPMain(args []string) {
	fs := flag.NewFlagSet("join-mcp", flag.ExitOnError)
	name := fs.String("name", defaultAgentName(), "name your session is addressed by in the party (@name)")
	server := fs.String("server", "partyline-party", "MCP server name to register")
	scope := fs.String("scope", "local", "claude mcp scope: local | project | user")
	printOnly := fs.Bool("print", false, "print the setup for any tool instead of running `claude mcp add`")
	// Allow flags and the link in any order — Go's flag package stops at the first
	// positional, so re-parse around it (the standard interspersed-args idiom).
	var link string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			fatal(err)
		}
		if fs.NArg() == 0 {
			break
		}
		if link == "" {
			link = fs.Arg(0)
		}
		rest = fs.Args()[1:]
	}
	if link == "" {
		fatal(fmt.Errorf("usage: ptln join-mcp '<party link>' [--name you] [--print]\n  the link is the one from the party page: https://partyline.sh/p/<id>#t=<token>"))
	}

	base, id, token, err := parsePartyLink(link)
	if err != nil {
		fatal(err)
	}
	exe := selfExe()
	env := [][2]string{
		{"PARTYLINE_PARTY_BASE", base},
		{"PARTYLINE_PARTY_ID", id},
		{"PARTYLINE_PARTY_TOKEN", token},
		{"PARTYLINE_AGENT_NAME", *name},
	}

	if *printOnly || !haveClaude() {
		printMCPSetup(*server, exe, env)
		if !*printOnly {
			fmt.Println("\n(`claude` isn't on your PATH, so I printed the setup instead of running it.)")
		}
		return
	}

	// claude mcp add <server> --scope <scope> --env K=V ... -- <exe> party-mcp
	cargs := []string{"mcp", "add", *server, "--scope", *scope}
	for _, kv := range env {
		cargs = append(cargs, "--env", kv[0]+"="+kv[1])
	}
	cargs = append(cargs, "--", exe, "party-mcp")
	cmd := exec.Command("claude", cargs...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fatal(fmt.Errorf("`claude mcp add` failed: %w\n\nRun `ptln join-mcp '<link>' --print` to set it up manually.", err))
	}

	fmt.Printf("\n✓ Registered MCP server %q for this project (scope: %s) as @%s.\n", *server, *scope, *name)
	fmt.Println("In your running session, run /mcp to connect it (or `claude --continue` to resume this session with it attached).")
	fmt.Println("Then just say: \"read the channel, then post a hello.\" Address others with @name.")
}

func haveClaude() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

var nameSafe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// defaultAgentName derives a sensible @handle from the OS user (or hostname), sanitized
// to the addressing charset. Falls back to "me".
func defaultAgentName() string {
	cand := ""
	if u, err := user.Current(); err == nil && u.Username != "" {
		cand = u.Username
	}
	if cand == "" {
		cand, _ = os.Hostname()
	}
	cand = nameSafe.ReplaceAllString(cand, "-")
	cand = strings.Trim(cand, "-")
	if cand == "" {
		return "me"
	}
	return cand
}

// printMCPSetup shows ready-to-paste setup for Claude, Codex, and a generic .mcp.json — for
// --print or when `claude` isn't installed.
func printMCPSetup(server, exe string, env [][2]string) {
	var claudeEnv, jsonEnv, codexEnv strings.Builder
	for i, kv := range env {
		fmt.Fprintf(&claudeEnv, " --env %s=%s", kv[0], shellQuote(kv[1]))
		if i > 0 {
			jsonEnv.WriteString(", ")
			codexEnv.WriteString("\n  ")
		}
		fmt.Fprintf(&jsonEnv, "%q: %q", kv[0], kv[1])
		fmt.Fprintf(&codexEnv, "mcp_servers.%s.env.%s = %q", server, kv[0], kv[1])
	}

	fmt.Println("Add the partyline party MCP server to your session:")
	fmt.Println()
	fmt.Println("Claude Code:")
	fmt.Printf("  claude mcp add %s --scope local%s -- %s party-mcp\n\n", server, claudeEnv.String(), exe)
	fmt.Println("Any tool that reads .mcp.json:")
	fmt.Printf("  {\"mcpServers\":{%q:{\"command\":%q,\"args\":[\"party-mcp\"],\"env\":{%s}}}}\n\n", server, exe, jsonEnv.String())
	fmt.Println("Codex (~/.codex/config.toml or -c overrides):")
	fmt.Printf("  mcp_servers.%s.command = %q\n  mcp_servers.%s.args = [\"party-mcp\"]\n  %s\n", server, exe, server, codexEnv.String())
	fmt.Println("\nThen connect it in your session (Claude: /mcp) and tell it to read the channel and post.")
}

func shellQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'$`\\") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
