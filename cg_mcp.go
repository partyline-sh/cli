package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// cg-mcp — a stdio MCP server (Common Ground) that exposes ONE thread's shared-context feed
// to an AI engine as tools: recall (read the shared decisions/constraints/contracts) and
// remember (record a seam fact others will see). Spawned by the engine, not a human — the
// mux wires it at launch and passes the active thread + identity via env.
//
// Auth: the recorded pragmatic compromise (COMMON-GROUND.md §10, Option A) — it uses the
// USER'S ACCOUNT TOKEN (api.New() → ~/.partyline/token); RLS scopes every read/write to the
// user's team. Speaks JSON-RPC 2.0 over stdin/stdout (newline-delimited); logs only to stderr.
func cgMCPMain(_ []string) {
	s := &cgServer{
		c:      api.New(),
		thread: strings.TrimSpace(os.Getenv("PARTYLINE_THREAD_ID")),
		agent:  strings.TrimSpace(os.Getenv("PARTYLINE_AGENT_NAME")),
		engine: strings.TrimSpace(os.Getenv("PARTYLINE_ENGINE")),
	}
	// One-shot presence ping: cg-mcp boots once per connected session, so this fires exactly when
	// a session connected to a thread starts — the web then shows who's using this context.
	// Fire-and-forget; a presence failure must never delay serving the engine.
	if s.thread != "" && api.LoadToken() != "" {
		go func() { _ = s.c.MarkConnected(s.thread, s.engine) }()
	}
	s.serve(os.Stdin, os.Stdout)
}

type cgServer struct {
	c                     *api.Client
	thread, agent, engine string
}

var cgToolDefs = []map[string]any{
	{
		"name":        "recall",
		"description": "Read the SHARED context for this thread — the decisions, constraints, API contracts, and open questions that teammates (and their agents) have recorded. Call this when you need to know what's already been settled across the boundary between people or components, before you assume or rebuild it.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "read_context",
		"description": "Alias for recall — the full current shared-context feed for this thread.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "remember",
		"description": "Record a durable SEAM fact to the shared thread so teammates' sessions see it: a decision, a constraint you hit, an API contract, an open question, or a note. Record these AS THEY HAPPEN — the moment a decision is made, a constraint surfaces, or a contract is agreed — don't wait to be asked. Use it only for things that cross the boundary between people or components (durable, cross-seam facts), never your own local working notes, chatter, or routine steps.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":       map[string]any{"type": "string", "enum": []string{"overview", "decision", "constraint", "contract", "question", "note"}, "description": "the kind of fact. 'overview' is the singular 'what this project/effort is about' frame — write/supersede one; the rest are atomic seam facts."},
				"body":       map[string]any{"type": "string", "description": "the fact itself, one or two sentences"},
				"supersedes": map[string]any{"type": "integer", "description": "the # of an existing block this UPDATES — it replaces the old value (kept in history). Use this instead of adding a contradicting fact. Get #s from recall."},
			},
			"required": []string{"kind", "body"},
		},
	},
}

// cgPromptDefs — MCP prompts, which clients like Claude Code surface as slash commands
// (/mcp__common-ground__…). "seed_from_history" backfills the thread from the current session:
// the agent already holds the whole conversation, so it just reviews + records (Model A — the
// user's own LLM, no scribe, no partyline model cost).
var cgPromptDefs = []map[string]any{
	{
		"name":        "seed_from_history",
		"description": "Review this session's conversation and record its durable decisions, constraints, and contracts to the shared Common Ground thread (seed the thread from your history so far).",
	},
	{
		"name":        "import_context",
		"description": "Pull the latest shared context for this thread into the conversation now and adopt it as background — use when facts may have changed or you just attached.",
	},
}

func (s *cgServer) serve(in io.Reader, out io.Writer) {
	r := bufio.NewReader(in)
	enc := json.NewEncoder(out)
	for {
		line, err := r.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			s.handle(t, enc)
		}
		if err != nil {
			return // EOF / closed pipe — the engine is done
		}
	}
}

func (s *cgServer) handle(line []byte, enc *json.Encoder) {
	var req rpcReq // rpcReq/rpcResp/rpcError are shared with party_mcp.go (same package)
	if json.Unmarshal(line, &req) != nil {
		return
	}
	notification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		ver := "2025-06-18"
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
			ver = p.ProtocolVersion
		}
		s.reply(enc, req.ID, map[string]any{
			"protocolVersion": ver,
			"capabilities":    map[string]any{"tools": map[string]any{}, "prompts": map[string]any{}},
			"serverInfo":      map[string]any{"name": "partyline-common-ground", "version": "0.1"},
		})
	case "notifications/initialized", "notifications/cancelled":
		// no response
	case "ping":
		s.reply(enc, req.ID, struct{}{})
	case "tools/list":
		s.reply(enc, req.ID, map[string]any{"tools": cgToolDefs})
	case "tools/call":
		s.handleCall(enc, req)
	case "prompts/list":
		s.reply(enc, req.ID, map[string]any{"prompts": cgPromptDefs})
	case "prompts/get":
		s.handlePromptGet(enc, req)
	default:
		if !notification {
			s.replyErr(enc, req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

// handlePromptGet returns the messages for an MCP prompt (surfaced as a slash command). The
// "seed_from_history" prompt injects a user-turn asking the agent to distill THIS conversation
// (which it already holds) into seam-facts via recall/remember — Model A backfill, no scribe.
func (s *cgServer) handlePromptGet(enc *json.Encoder, req rpcReq) {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &p)

	noThread := "This session isn't attached to a Common Ground thread yet. Attach one (ctrl-\\ c " +
		"in the partyline session manager, or relaunch with `ptln new <tool> --thread <id>`), then run this again."
	var desc, text string
	switch p.Name {
	case "seed_from_history":
		desc = "Seed the shared thread from this session's history"
		if s.thread == "" {
			text = noThread
		} else {
			text = "Bring the shared Common Ground thread up to date from our conversation so far.\n\n" +
				"1. First call `recall` to see what's already recorded — don't duplicate it.\n" +
				"2. Write or update the OVERVIEW: one `remember` with kind='overview' — 2–4 sentences on " +
				"what this project/effort is, who it's for, and its shape. This is the orientation a " +
				"newcomer reads before the details. If an overview already exists, `supersedes` it.\n" +
				"3. Then review our whole conversation and identify the DURABLE, CROSS-SEAM facts: " +
				"decisions made, hard constraints, and API/interface contracts another person or " +
				"component will depend on.\n" +
				"4. For each, call `remember` (kind = decision | constraint | contract | question; one " +
				"concise sentence). If a fact updates something already recorded, use `supersedes`.\n" +
				"5. Skip chatter, routine steps, and anything scoped only to this session.\n\n" +
				"When you're done, briefly list what you recorded."
		}
	case "import_context":
		desc = "Pull the latest shared context into the conversation"
		if s.thread == "" {
			text = noThread
		} else {
			text = "Pull the latest shared team context for this thread and adopt it. Call the " +
				"common-ground `recall` tool now, treat what it returns as background you already know " +
				"(do NOT act on it or change anything unless I ask), and briefly note anything that's " +
				"new or changed since we last looked. Use it going forward."
		}
	default:
		s.replyErr(enc, req.ID, -32602, "unknown prompt: "+p.Name)
		return
	}
	s.reply(enc, req.ID, map[string]any{
		"description": desc,
		"messages": []map[string]any{
			{"role": "user", "content": map[string]any{"type": "text", "text": text}},
		},
	})
}

func (s *cgServer) handleCall(enc *json.Encoder, req rpcReq) {
	var p struct {
		Name string `json:"name"`
		Args struct {
			Kind       string `json:"kind"`
			Body       string `json:"body"`
			Supersedes int64  `json:"supersedes"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)
	if s.thread == "" {
		s.toolResult(enc, req.ID, "No thread is attached to this session (PARTYLINE_THREAD_ID is unset). Start it with: ptln new <tool> --thread <id>.", true)
		return
	}
	switch p.Name {
	case "recall", "read_context":
		blocks, err := s.c.Recall(s.thread, 0)
		if err != nil {
			s.toolResult(enc, req.ID, "recall failed: "+err.Error(), true)
			return
		}
		s.toolResult(enc, req.ID, formatContextBlocks(blocks), false)
	case "remember":
		if strings.TrimSpace(p.Args.Body) == "" {
			s.toolResult(enc, req.ID, "remember needs a non-empty `body`.", true)
			return
		}
		kind := p.Args.Kind
		if kind == "" {
			kind = "note"
		}
		b, err := s.c.Remember(s.thread, kind, p.Args.Body, s.agent, s.engine, p.Args.Supersedes)
		if err != nil {
			s.toolResult(enc, req.ID, "remember failed: "+err.Error(), true)
			return
		}
		s.toolResult(enc, req.ID, "Recorded to the shared thread ("+b.Kind+", #"+strconv.FormatInt(b.ID, 10)+").", false)
	default:
		s.replyErr(enc, req.ID, -32602, "unknown tool: "+p.Name)
	}
}

// formatContextBlocks renders the feed for an LLM. The 'overview' (what this is about) leads —
// orientation before the atomic facts — then decisions/constraints/contracts oldest-first.
// superseded/proposed/pruned are hidden (they're history / unconfirmed / retracted).
func formatContextBlocks(blocks []api.ContextBlock) string {
	live := func(blk api.ContextBlock) bool {
		return blk.Status != "superseded" && blk.Status != "proposed" && blk.Status != "pruned"
	}
	line := func(b *strings.Builder, blk api.ContextBlock) {
		fmt.Fprintf(b, "\n• #%d [%s] %s\n  — %s", blk.ID, blk.Kind, blk.Body, blk.Author)
		if blk.Engine != "" {
			b.WriteString(" (" + blk.Engine + ")")
		}
	}
	var overview, rest strings.Builder
	nOv, nRest := 0, 0
	for _, blk := range blocks {
		if !live(blk) {
			continue
		}
		if blk.Kind == "overview" {
			line(&overview, blk)
			nOv++
		} else {
			line(&rest, blk)
			nRest++
		}
	}
	if nOv == 0 && nRest == 0 {
		return "No shared context has been recorded on this thread yet."
	}
	var b strings.Builder
	if nOv > 0 {
		b.WriteString("What this is about:")
		b.WriteString(overview.String())
	}
	if nRest > 0 {
		if nOv > 0 {
			b.WriteString("\n")
		}
		b.WriteString("\nShared context for this thread:")
		b.WriteString(rest.String())
	}
	return b.String()
}

func (s *cgServer) reply(enc *json.Encoder, id json.RawMessage, result interface{}) {
	_ = enc.Encode(rpcResp{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *cgServer) replyErr(enc *json.Encoder, id json.RawMessage, code int, msg string) {
	_ = enc.Encode(rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *cgServer) toolResult(enc *json.Encoder, id json.RawMessage, text string, isErr bool) {
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if isErr {
		res["isError"] = true
	}
	s.reply(enc, id, res)
}
