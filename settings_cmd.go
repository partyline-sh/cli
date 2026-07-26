package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// ptln settings — the MASTER INDEX of every partyline setting surface (#577): one command that
// shows the current state of each area and exactly where to change it (CLI command, mux chord,
// or web page). Read-only by design — each area keeps its own editor; this is the map.
func settingsMain(_ []string) {
	fmt.Println("partyline settings — current state · where to change it")
	fmt.Println()

	// Account
	if api.LoadToken() == "" {
		fmt.Println("  account        not logged in                        → ptln login")
	} else if me, err := api.New().Me(); err == nil {
		fmt.Printf("  account        %s (%s)%s\n", me.Handle, me.Email, "                → ptln whoami · ptln logout")
	} else {
		fmt.Println("  account        logged in (profile unreachable)      → ptln whoami")
	}

	// This repo's context thread
	cwd, _ := os.Getwd()
	if bound := loadRepoBind(cwd); bound != "" {
		fmt.Printf("  this repo      thread %s…  → ptln thread bind [--clear]\n", bound[:8])
	} else {
		fmt.Println("  this repo      no context thread bound              → ptln thread bind <id> (tools appear either way; they guide you)")
	}

	// MCP catalog (this machine)
	cat := loadMCPCatalog()
	if len(cat) == 0 {
		fmt.Println("  mcp catalog    empty                                → ctrl-\\ m inside ptln")
	} else {
		names := make([]string, 0, len(cat))
		for n := range cat {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Printf("  mcp catalog    %s  → ctrl-\\ m inside ptln\n", strings.Join(names, ", "))
	}

	// Agent tool grants (per project, per role)
	fmt.Println("  agent tools    per-project planning/build grants    → ptln project tools <label> · web: project page")

	// Daemon + registered projects
	reg := loadDaemonRegistry()
	fmt.Printf("  daemon         %d project(s) registered               → ptln daemon status · ptln daemon add-project\n", len(reg.Projects))

	// Engine wiring (persistent registrations for engines without per-launch hooks)
	fmt.Println("  engines        gemini/antigravity thread wiring     → ptln thread connect <engine>")

	// Web-only surfaces
	fmt.Println("  web            notifications · review gate · integrations · team → partyline.sh/settings")
	fmt.Println("                 models/engines per project · visual verify        → partyline.sh project pages")
}
