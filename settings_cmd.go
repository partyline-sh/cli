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

	// Peer consults, per project. BOTH DEFAULTS ARE WORTH STATING OUT LOUD, in opposite directions:
	// auto-answer is ON (a teammate's agent can spend a read-only turn here without asking you), and
	// auto-submit is OFF (a peer's answer would otherwise become a prompt in an agent that can write —
	// see peer_deliver.go). A master index that omitted them would hide the two settings on this
	// machine that another person can act through.
	autoAnswer, autoSubmit := 0, 0
	for _, p := range reg.Projects {
		if p.consultPolicy() == "auto" {
			autoAnswer++
		}
		if p.Deliver == "submit" {
			autoSubmit++
		}
	}
	// EFFECTIVE state, not the per-project field. The machine-wide switch outranks every project, so
	// printing "3/3 auto-answer" while the machine says no would be a lie in the one place that exists
	// to be believed.
	if globalConsultPolicy() != "auto" {
		fmt.Printf("  peer consults  OFF machine-wide — all %d suppressed        → ptln daemon consults --all auto (to allow again)\n", len(reg.Projects))
	} else {
		fmt.Printf("  peer consults  %d/%d auto-answer, read-only (default on)  → ptln daemon consults <label> [auto|ask] · --all ask\n", autoAnswer, len(reg.Projects))
	}
	fmt.Printf("  peer delivery  %d/%d auto-SUBMIT answers (default off)    → ptln daemon deliver <label> [stage|submit]\n", autoSubmit, len(reg.Projects))

	// Engine wiring (persistent registrations for engines without per-launch hooks)
	fmt.Println("  engines        gemini/antigravity thread wiring     → ptln thread connect <engine>")

	// Account surfaces, all reachable from here (#829). Listed as commands rather than as "go to
	// the web app", because this index is what an agent reads to find out how to configure
	// partyline — a pointer to a browser is a dead end for the caller most likely to be reading it.
	fmt.Println("  profile        name · handle · timezone · email       → ptln me [set]")
	fmt.Println("  notifications  what you hear about, and where         → ptln notify [on|off|quiet]")
	fmt.Println("  team           review gate · default engine/model · run caps → ptln team set")
	fmt.Println("  project docs   the globals injected into every run     → ptln project doc <label>")
	fmt.Println("  environments   the deploy chain, per project           → ptln project env <label>")
	fmt.Println("  triggers       inbound: things that start work here    → ptln trigger")
	fmt.Println("  webhooks       outbound: where events go               → ptln webhook")
	fmt.Println("  api keys       for CI and scripts                      → ptln key")
	fmt.Println("  skills         the org skill library                   → ptln skill")

	// The remainder genuinely has no CLI path yet — say which, rather than implying the list above
	// is the whole surface.
	fmt.Println("  web only       billing · Slack/GitHub integrations · members & roles → partyline.sh/settings")
}
