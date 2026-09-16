package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// server_skill.go — `ptln server skill`: give the AI on this box the manual for it.
//
// A server with ptln on it usually has a coding agent on it too, and "configure my partyline" is
// exactly the kind of task people hand that agent. The agent's problem is knowing the command
// surface and the red lines (never `down -v`, never print .env values). Both fit in a skill file,
// so the CLI carries one and installs it where Claude Code reads skills from.
//
// The file is embedded, not fetched: this must work on a box that has nothing but the binary,
// which is the same rule as the compose stack.

//go:embed assets/skills/partyline-server/SKILL.md
var serverSkillMD string

func serverSkillMain(args []string) {
	printOnly := false
	for _, a := range args {
		switch a {
		case "--print":
			// For engines that are not Claude Code: pipe it wherever their skills live.
			printOnly = true
		case "--help", "-h":
			serverUsage(os.Stdout)
			return
		default:
			fmt.Fprintf(os.Stderr, "ptln server skill: unknown argument %q (flags: --print)\n", a)
			os.Exit(2)
		}
	}
	if printOnly {
		fmt.Print(serverSkillMD)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	dir := filepath.Join(home, ".claude", "skills", "partyline-server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fatal(err)
	}
	dst := filepath.Join(dir, "SKILL.md")
	// Overwritten on purpose: the file ships with the binary and tracks it. Local edits belong
	// in a differently-named skill, not in this one.
	if err := os.WriteFile(dst, []byte(serverSkillMD), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("installed %s\n", dst)
	fmt.Println("Claude Code picks it up on its next session: ask it to configure or check this server.")
	fmt.Println("Other engines: ptln server skill --print")
}
