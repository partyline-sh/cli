package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// run_mcp_stop.go — `ptln run-mcp`: the run-side MCP server (#794 T1b), and the FIRST partyline
// tool a crank run has ever had. One tool: stop_run(reason).
//
// WHY IT EXISTS. A triggered agent's template carries a mandatory stop rule ("if you can't find
// the ticket id, stop and say so") — but until now there was NO CHANNEL to stop through. A crank
// worker gets only explicitly-granted MCP servers (work.go), so an agent whose stop rule fired had
// two options, both bad: improvise around the problem (the 3am wrong-write the stop rule exists to
// prevent), or just end its turn — which reports as a normal completion and is indistinguishable
// from success. T1a taught the whole web stack what run.stopped MEANS (terminal status, its own
// badge and notification, distinct from killed and failed); this is the tool that lets an agent
// actually declare it.
//
// CREDENTIALS BY INHERITANCE, nothing new threaded: crank runs with PARTYLINE_RUN_ID,
// PARTYLINE_DAEMON_TOKEN and PARTYLINE_API in its environment, the worker inherits them, and MCP
// subprocesses inherit the worker's. This server just reads them. Absent any of the three (a
// `ptln work` outside a daemon run), the tool reports that stopping-with-status isn't available —
// it never pretends to have recorded something it couldn't.
//
// The stopped status is TERMINAL in the daemon state machine, so crank's own later "done"/"failed"
// report for the same run is refused by the status route — the deliberate stop wins the race by
// design, not by timing.

// runStopReporter is the seam the tests replace: report status `stopped` with the agent's reason.
var runStopReporter = func(base, token, runID, reason string) error {
	return api.SetRunStatus(base, token, runID, "stopped", reason)
}

// runMCPStopResult is the tool's outcome text, extracted pure so the three cases — recorded,
// unavailable, failed — are testable as a rule. `ok` mirrors MCP's isError.
func runMCPStopResult(runID, token, base, reason string, report func(base, token, runID, reason string) error) (text string, isErr bool) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "stop_run needs a reason — the whole point of a deliberate stop is that a human reads WHY. Call it again with one plain sentence.", true
	}
	if runID == "" || token == "" {
		// Honest unavailability beats a fake success: the agent should still stop, and say why in
		// its final answer — a human reads that instead of the status.
		return "This session isn't a daemon-managed run, so there is no run status to set. Stop working now and state your reason as your final answer instead.", true
	}
	if err := report(base, token, runID, reason); err != nil {
		return "Could not record the stop (" + err.Error() + "). Stop working anyway and state your reason as your final answer — a human must see it.", true
	}
	return "Recorded: this run is now marked STOPPED with your reason, and the team is being told. Do no further work — end your turn now with a one-line restatement of why you stopped.", false
}

// runMCPMain is the stdio loop — deliberately minimal (initialize, tools/list, tools/call), the
// same protocol subset cg-mcp speaks, without its thread machinery.
func runMCPMain() {
	runID := strings.TrimSpace(os.Getenv("PARTYLINE_RUN_ID"))
	token := strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	base := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	if base == "" {
		base = api.Base()
	}

	enc := json.NewEncoder(os.Stdout)
	reply := func(id json.RawMessage, result any) {
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	toolText := func(id json.RawMessage, text string, isErr bool) {
		res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		if isErr {
			res["isError"] = true
		}
		reply(id, res)
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		if len(req.ID) == 0 { // notification — no reply
			continue
		}
		switch req.Method {
		case "initialize":
			ver := "2025-06-18"
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
				ver = p.ProtocolVersion
			}
			reply(req.ID, map[string]any{
				"protocolVersion": ver,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "partyline-run", "version": "0.1"},
			})
		case "tools/list":
			reply(req.ID, map[string]any{"tools": []map[string]any{{
				"name": "stop_run",
				"description": "Deliberately STOP this run, with a reason a human will read. Use this when your " +
					"instructions' stop rule applies, or when you cannot safely proceed (a required tool is missing, " +
					"the input doesn't contain what you need, the action would be a guess). Stopping deliberately is " +
					"a first-class outcome, not a failure — it is ALWAYS better than improvising a plausible wrong " +
					"action. After calling this, end your turn immediately.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"reason": map[string]any{"type": "string", "description": "one or two plain sentences: what you were asked, what was missing or wrong, why proceeding would be unsafe."},
					},
					"required": []string{"reason"},
				},
			}}})
		case "tools/call":
			var p struct {
				Name string `json:"name"`
				Args struct {
					Reason string `json:"reason"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name != "stop_run" {
				toolText(req.ID, fmt.Sprintf("unknown tool %q — this server has exactly one tool, stop_run.", p.Name), true)
				continue
			}
			text, isErr := runMCPStopResult(runID, token, base, p.Args.Reason, runStopReporter)
			toolText(req.ID, text, isErr)
		default:
			// Unknown request: an empty result keeps strict clients moving; there is nothing here
			// to negotiate.
			reply(req.ID, map[string]any{})
		}
	}
}
