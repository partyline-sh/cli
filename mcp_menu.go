package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// mcpSessionMenu is the `ctrl-\ m` overlay: wire/unwire MCP servers for the FOCUSED session.
// partyline's own servers are pinned at the top — attaching context threads (the shared brain)
// is one keypress away — and below them the user's catalog (~/.partyline/mcp.json). Toggles are
// batched: flip any number of servers, then leaving the menu applies them in ONE relaunch
// (queued via SetPendingReattach, same contract as the ctrl-\ c menu). Uses the cg_menu palette.
func mcpSessionMenu(mx sessionMenuTarget) {
	in := bufio.NewReader(os.Stdin)
	argv, dir, label, key, ok := mx.ActiveLaunch()
	if !ok || len(argv) == 0 {
		cgFrame("MCP servers")
		fmt.Printf("  %sopen a session first — MCP servers wire into a live session.%s\n", cgDim, cgOff)
		pause(in)
		return
	}
	bin := argv[0]
	perLaunch := bin == "claude" || bin == "codex" // engines with per-launch MCP flags
	thread, wired := mx.ActiveThreadInfo()
	cur := mx.ActiveMCPs()
	desired := map[string]bool{}
	for _, n := range cur {
		desired[n] = true
	}
	changed := false

	// The attached thread's title, for the pinned row + the reattach Spec (one fetch, not per redraw).
	threadTitle := thread
	if thread != "" && api.LoadToken() != "" {
		if th, _, err := api.New().GetThread(thread); err == nil && th != nil {
			threadTitle = th.Title
		}
	}

	// apply relaunches the session with the new server set (claude/codex only). An attached
	// thread rides along — and since we're restarting anyway, a record-only thread comes back
	// WIRED (strict upgrade: the agent gains recall/remember for it).
	apply := func() {
		if !changed || !perLaunch {
			return
		}
		var mcps []string
		for _, n := range loadMCPCatalog().names() {
			if desired[n] {
				mcps = append(mcps, n)
			}
		}
		wiredArgv, eng := wireSessionArgv(bin, carryConversation(bin, argv), thread, mcps)
		fmt.Printf("\n  %sRestarting your session%s with the new MCP set…\n", cgBold, cgOff)
		fmt.Printf("  %syour conversation carries over; give the agent a moment to reconnect.%s\n", cgDim, cgOff)
		mx.SetPendingReattach(ptymux.Spec{Label: label, Key: key + "·mcp", Argv: wiredArgv, Dir: dir, Thread: thread, ThreadLabel: threadTitle, Engine: eng, MCPs: mcps})
		pause(in)
	}

	for {
		cat := loadMCPCatalog()
		names := cat.names()
		cgFrame("MCP servers")
		fmt.Printf("  session: %s%s%s\n\n", cgBold, label, cgOff)

		// Pinned partyline servers — the moat, one keypress away.
		fmt.Printf("  %spartyline%s\n", cgDim, cgOff)
		switch {
		case thread != "" && wired:
			fmt.Printf("    %s●%s context threads — attached: %s%s%s  %s(wired)%s\n", cgWire, cgOff, cgBold, threadTitle, cgOff, cgDim, cgOff)
		case thread != "":
			fmt.Printf("    %s●%s context threads — recording to: %s%s%s  %s(agent not wired — c wires it)%s\n", cgDim, cgOff, cgBold, threadTitle, cgOff, cgDim, cgOff)
		default:
			fmt.Printf("    %s○%s context threads — give this agent a shared memory  %s(c attaches one)%s\n", cgDim, cgOff, cgDim, cgOff)
		}
		fmt.Printf("    %s○%s party — bring this agent into a team party  %s(launcher: ptln party)%s\n", cgDim, cgOff, cgDim, cgOff)

		// The user's catalog.
		fmt.Printf("\n  %scatalog%s  %s%s%s\n", cgDim, cgOff, cgDim, mcpCatalogPath(), cgOff)
		if len(names) == 0 {
			fmt.Printf("    %s(no servers yet — a adds one)%s\n", cgDim, cgOff)
		}
		show := names
		if len(show) > 9 {
			show = show[:9] // single-digit toggles; the rest is file-edit territory
		}
		for i, n := range show {
			dot, clr := "○", cgDim
			if desired[n] {
				dot, clr = "●", cgWire
			}
			note := cat[n].Command
			if cat[n].URL != "" {
				note = cat[n].URL
				if bin == "codex" {
					note += " — claude only"
				}
			}
			fmt.Printf("    %s%d%s %s%s%s %s  %s%s%s\n", cgKey, i+1, cgOff, clr, dot, cgOff, n, cgDim, note, cgOff)
		}
		if len(names) > 9 {
			fmt.Printf("    %s(+%d more — edit the file)%s\n", cgDim, len(names)-9, cgOff)
		}

		fmt.Println()
		switch {
		case perLaunch:
			cgItem("1-9", "toggle a server", "applies on exit (restarts the session)")
		case isWireableEngine(bin):
			fmt.Printf("    %s%s wires MCPs in its own config — one-time: `%s mcp add …` · context: `ptln thread connect %s`%s\n", cgDim, bin, bin, bin, cgOff)
		default:
			fmt.Printf("    %sthis is a shell — open an AI session (claude/codex) to wire MCP servers%s\n", cgDim, cgOff)
		}
		cgItem("a", "add a server to the catalog", "")
		cgItem("c", "context threads menu", "attach/record/view (ctrl-\\ c)")
		// Unapplied toggles are the one thing that makes the exit non-obvious, so the pill
		// changes with them rather than the exit row silently meaning two different things.
		if changed {
			fmt.Println("  " + brand.HintBar("MCP · UNAPPLIED", []brand.Hint{
				{Key: "q · esc · ⏎", Label: "apply + back to your session"}}, 0))
		} else {
			cgHintPrint("MCP")
		}
		sel := string(menuKey()) // single keypress (already lowercased)
		switch {
		case sel == "q" || sel == "\n" || sel == "\x00": // q / enter / esc → apply + close
			apply()
			return
		case sel == "c":
			// Hand off to the context menu; it may queue its own reattach (picked up by the
			// ctrl-\ m handler). Unapplied toggles here are dropped — one restart at a time.
			cgSessionMenu(mx)
			return
		case sel == "a":
			mcpAddServer(in)
		case len(sel) == 1 && sel[0] >= '1' && sel[0] <= '9':
			i := int(sel[0] - '1')
			if i >= len(show) {
				continue
			}
			if !perLaunch {
				fmt.Printf("\n  %stoggles need a per-launch engine (claude/codex) — this session is %s.%s\n", cgDim, bin, cgOff)
				pause(in)
				continue
			}
			n := show[i]
			if bin == "codex" && cat[n].URL != "" {
				fmt.Printf("\n  %s%s is an HTTP server — codex can't wire those per-launch (claude only).%s\n", cgDim, n, cgOff)
				pause(in)
				continue
			}
			desired[n] = !desired[n]
			changed = true
		}
	}
}

// mcpAddServer prompts for a catalog entry: a name plus either a command line or an http(s) URL.
func mcpAddServer(in *bufio.Reader) {
	fmt.Print("\n  name › ")
	name, _ := in.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if strings.ContainsAny(name, " .\"'") {
		fmt.Printf("  %sname must be a bare identifier (no spaces/dots) — it becomes a config key.%s\n", cgDim, cgOff)
		pause(in)
		return
	}
	fmt.Printf("  command or URL %s(e.g. npx -y @upstash/context7-mcp · https://…)%s › ", cgDim, cgOff)
	raw, _ := in.ReadString('\n')
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	def := mcpDef{}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		def.URL = raw
	} else {
		f := strings.Fields(raw)
		def.Command = f[0]
		if len(f) > 1 {
			def.Args = f[1:]
		}
	}
	cat := loadMCPCatalog()
	cat[name] = def
	if err := saveMCPCatalog(cat); err != nil {
		fmt.Printf("  %s✗ %v%s\n", cgBad, err, cgOff)
		pause(in)
		return
	}
	fmt.Printf("  %s✓%s %s saved — toggle it on with its number.\n", cgOK, cgOff, name)
	pause(in)
}
