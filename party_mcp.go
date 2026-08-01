package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
)

// party-mcp is a minimal stdio MCP server (Epic B) that exposes the partyline party
// channel to an AI engine as structured tools — so an agent reads the shared doc /
// channel and proposes edits via real tool calls instead of the text-floor convention.
//
// It is spawned by the engine, not by a human: the runner wires it into the engine's
// MCP config (claude --mcp-config, codex -c mcp_servers.*, gemini settings.json) and
// passes the party-scoped token + identity via env. So it speaks JSON-RPC 2.0 over
// stdin/stdout (newline-delimited per the MCP stdio transport) and logs ONLY to stderr
// — anything on stdout that isn't a JSON-RPC message corrupts the protocol.
//
// Boundary: every tool calls the partyline HTTP API with the party token (reusing
// PartyClient). This is client-side code on the agent's machine — it never touches the
// DB, and the token scopes it to exactly one party.
func partyMCPMain(_ []string) {
	pc := &api.PartyClient{
		Base:  envOr("PARTYLINE_PARTY_BASE", api.Base()),
		ID:    os.Getenv("PARTYLINE_PARTY_ID"),
		Token: strings.TrimSpace(os.Getenv("PARTYLINE_PARTY_TOKEN")),
	}
	name := envOr("PARTYLINE_AGENT_NAME", "agent")
	(&mcpServer{pc: pc, name: name}).serve(os.Stdin, os.Stdout)
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

type mcpServer struct {
	pc   *api.PartyClient
	name string
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent → notification (never replied to)
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// serve reads newline-delimited JSON-RPC requests until the engine closes the pipe.
func (s *mcpServer) serve(in io.Reader, out io.Writer) {
	r := bufio.NewReader(in)
	enc := json.NewEncoder(out)
	for {
		line, err := r.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			s.handle(t, enc)
		}
		if err != nil {
			return // EOF / closed pipe → engine is done with us
		}
	}
}

func (s *mcpServer) handle(line []byte, enc *json.Encoder) {
	var req rpcReq
	if err := json.Unmarshal(line, &req); err != nil {
		return // malformed; no id to respond to
	}
	notification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		s.reply(enc, req.ID, s.initResult(req.Params))
	case "notifications/initialized", "notifications/cancelled":
		// notifications: no response
	case "ping":
		s.reply(enc, req.ID, struct{}{})
	case "tools/list":
		s.reply(enc, req.ID, map[string]any{"tools": toolDefs})
	case "tools/call":
		s.handleToolCall(enc, req)
	default:
		if !notification {
			s.replyErr(enc, req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

// initResult echoes the client's requested protocol version (falls back to a known one)
// and advertises only the tools capability.
func (s *mcpServer) initResult(params json.RawMessage) map[string]any {
	ver := "2025-06-18"
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		ver = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": ver,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "partyline-party", "version": "0.1"},
	}
}

// partylineHelpDoc is returned by the `help` tool so a connected LLM can tell its human
// exactly how to use partyline — connect another tool, the commands, what each tool does.
const partylineHelpDoc = `partyline — how to use it (relay this to the human when they ask "how do I…").

CONNECT A TOOL TO A PARTY (from a join link  https://partyline.sh/p/<id>#t=<token>):
• Claude Code (easiest):  ptln join-mcp '<link>' --name you       then run /mcp to connect
• Any other tool:         ptln join-mcp '<link>' --name you --print
    prints ready-to-paste config for Codex (~/.codex/config.toml), Gemini (~/.gemini/settings.json),
    and a generic .mcp.json. The server is the command "ptln party-mcp" with these env vars:
      PARTYLINE_PARTY_BASE=https://partyline.sh   PARTYLINE_PARTY_ID=<id from the link path>
      PARTYLINE_PARTY_TOKEN=<token after #t=>      PARTYLINE_AGENT_NAME=<your @name>

THREE WAYS TO JOIN:
• Fresh dedicated agent :  ptln party '<link>' --name db     (a new agent; auto-responds when @'d)
• Your current session  :  ptln join-mcp '<link>'            (tools in your session; you pilot it)
• Send a copy of yourself: ptln party '<link>' --clone       (wires MCP into your session + spawns a
                                                              context-carrying clone that auto-responds)

MCP TOOLS YOU CAN CALL HERE:
• read_channel     recent messages in this party
• read_transcript  the FULL discussion (works even after the party closes)
• post             send a message — address people with @name
• who              who's recently active
• read_doc / propose_edit   read / propose a change to the shared decision doc (a human approves)
• plan_read        this party's planning tree (epics ▸ features ▸ tasks) with status + readiness
• plan_upsert / plan_move   draft new plan items or restructure the tree (drafts only — humans promote)
• plan_propose     propose promoting a task to the Build backlog or archiving an item (a human approves)
• skill_list       discover your org's ENABLED skills (name + description + version — no bodies)
• skill_fetch      pull one org skill's full SKILL.md body by name (e.g. mid-run, "I need the deploy skill")
• ask_human        flag something that needs a person to decide
• help             this doc

GROUNDED (approach-review) PARTIES: agents answer in cited "position" blocks; partyline re-fetches each
cited source and an independent model verifies it — only verified positions post (shown as cards). Bring
one with:  ptln party '<link>' --name expert --evidence -- --allowedTools "Read,Grep,Glob"

Full docs: https://partyline.sh/docs/parties
`

var toolDefs = []map[string]any{
	{
		"name": "read_channel",
		"description": "Read recent messages in the party channel — what the humans and other agents " +
			"(including other developers' live coding sessions) have said. Call this when you join the " +
			"conversation, and again before you post, so you're responding to the latest. Returns recent " +
			"messages oldest-first with who said each.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		"name": "read_transcript",
		"description": "Read the FULL discussion transcript of the party — the complete record, not just " +
			"recent messages. Use this to catch up on or summarize everything that was said, including " +
			"AFTER the party has ended (the parent session pulling the artifacts back). Returns markdown, " +
			"oldest first.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		"name": "post",
		"description": "Post a message to the party channel — everyone in the room sees it (the humans and " +
			"other agents/sessions). Use it to share what you're working on or found, answer a question, or " +
			"coordinate with another developer's session. Address someone with @name (or @all / @any). Keep " +
			"it concise — this is a shared chat, not a monologue — and read_channel first so you reply to the latest.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string", "description": "The message to post. Address someone with @name."},
			},
			"required": []string{"message"},
		},
	},
	{
		"name": "who",
		"description": "List who's recently active in the party (humans and agents) so you know who you can " +
			"address with @name.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		"name": "help",
		"description": "Get partyline usage docs — how to connect another LLM/tool to a party, the commands, " +
			"and what each tool does. Call this when the human asks how to do something with partyline (e.g. " +
			"\"how do I bring another agent in?\") so you can tell them exactly what to run.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		"name": "read_doc",
		"description": "Read the party's shared working doc (the room's living markdown artifact — " +
			"a PRD, incident timeline, or decision log). Returns the current version and body. " +
			"Read this before proposing changes so you build on the latest text, not a stale copy.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		"name": "propose_edit",
		"description": "Propose a change to ONE section of the shared working doc. A human reviews and " +
			"approves before it merges — your edit does NOT take effect immediately, so don't assume it's " +
			"applied. Give the FULL new contents of the section: it replaces that section, or is appended " +
			"as a new section if the heading doesn't exist yet. Call read_doc first so you edit the latest.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"section":  map[string]any{"type": "string", "description": `The section heading to add or replace, e.g. "Risks" or "Open Questions".`},
				"new_body": map[string]any{"type": "string", "description": "The complete new contents for that section (markdown, without the heading line)."},
			},
			"required": []string{"section", "new_body"},
		},
	},
	{
		"name": "ask_human",
		"description": "Post a question that explicitly needs a human decision or input — use when you're " +
			"blocked, or something is risky, ambiguous, or irreversible. It's flagged for humans in the " +
			"channel. Use sparingly: for genuine decisions, not routine chat (just reply normally for that).",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "The question or decision you need a human to weigh in on."},
			},
			"required": []string{"question"},
		},
	},
	{
		"name": "propose_fix",
		"description": "Propose a concrete code fix for a human to approve. Use this ONLY once you've " +
			"investigated the actual code and refined the report into a precise, LOCATED task with a " +
			"checkable acceptance criterion (see the fix-intake flow). Approving it opens a reviewable PR " +
			"via a gated run — it does NOT merge anything, and you do NOT write the code here. Don't propose " +
			"vague or risky tasks: keep refining (or say an engineer should drive) instead.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task":       map[string]any{"type": "string", "description": "The precise, located task — what to change and where (file/function), in one clear instruction."},
				"acceptance": map[string]any{"type": "string", "description": "How we'll know it's fixed — a concrete, checkable done-condition."},
			},
			"required": []string{"task", "acceptance"},
		},
	},
	{
		"name": "plan_read",
		"description": "Read this party's planning tree (epics ▸ features ▸ tasks) with status, readiness " +
			"and notes. Use before any plan edit.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		// The two false claims that motivated this pair: "I have no GitHub tool wired up" (it did) and
		// "the /work board is not reachable" (it holds plan_read). An agent that can LOOK UP its reach
		// and the real backlog stops handing the question back to the human.
		"name": "capabilities",
		"description": "Look up exactly what you can do in this session — your tools, the shell commands and MCP servers " +
			"granted for this project, and their SCOPE limits. Call this instead of guessing at your own reach, and call it " +
			"again if a human says you should be able to do something you think you can't: grants can change mid-conversation.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		"name": "backlog_read",
		"description": "Read the TEAM's Build backlog and in-flight work: what's queued (in run order), what's building, " +
			"what needs attention (failed or awaiting approval), and what shipped recently. This is the org-wide factory " +
			"board — distinct from plan_read, which is only THIS conversation's plan. Use it before proposing work, so you " +
			"don't re-propose something already queued, running, or failed twice.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		"name": "plan_upsert",
		"description": "Create a plan item (omit `id`) or edit an existing one (pass `id`). LIMITS: items " +
			"you create start as DRAFTS a human must promote; the server refuses edits to items that are " +
			"building/done or that belong to this session's own ## Plan subtree; changing readiness requires " +
			"a one-line readiness_note explaining why. Call plan_read first so you work from the latest tree.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":       map[string]any{"type": "string", "description": "Existing item id to edit. Omit to create a new item."},
				"kind":     map[string]any{"type": "string", "enum": []string{"epic", "feature", "task"}, "description": "Item kind (required when creating; cannot be changed on edit)."},
				"title":    map[string]any{"type": "string", "description": "Short item title (required when creating)."},
				"document": map[string]any{"type": "string", "description": "Longer markdown body/spec for the item."},
				"acceptance_criteria": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text":   map[string]any{"type": "string", "description": "The checkable done-condition."},
							"verify": map[string]any{"type": "string", "description": "How it's verified."},
						},
						"required": []string{"text", "verify"},
					},
					"description": "Checkable done-conditions for the item.",
				},
				"readiness":      map[string]any{"type": []string{"string", "number"}, "description": "New readiness value — must come with a readiness_note."},
				"readiness_note": map[string]any{"type": "string", "description": "One line explaining WHY readiness changed (required with readiness)."},
				"parent_id":      map[string]any{"type": "string", "description": "Parent item to create under (create only; use plan_move to reparent)."},
			},
		},
	},
	{
		"name": "plan_move",
		"description": "Move a plan item under a new parent and/or reorder it among its siblings " +
			"(task→feature, feature→epic; the server enforces which moves are allowed). Omit parent_id " +
			"(or pass null) to move the item to the top level.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":        map[string]any{"type": "string", "description": "The item to move."},
				"parent_id": map[string]any{"type": []string{"string", "null"}, "description": "New parent item id, or null/omitted for the top level."},
				"rank":      map[string]any{"type": "number", "description": "Position among the new siblings (lower sorts first). Omit to let the server place it."},
			},
			"required": []string{"id"},
		},
	},
	{
		"name": "plan_propose",
		"description": "Propose promoting a task to the Build backlog, or archiving a dead item. A HUMAN " +
			"approves before anything executes — you cannot promote or archive directly.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":  map[string]any{"type": "string", "enum": []string{"promote", "archive"}, "description": `"promote" (task → Build backlog) or "archive" (dead item).`},
				"item_id": map[string]any{"type": "string", "description": "The plan item the proposal is about."},
				"note":    map[string]any{"type": "string", "description": "Optional one-line rationale for the human reviewer."},
			},
			"required": []string{"action", "item_id"},
		},
	},
	// skill_list / skill_fetch are READ-ONLY: progressive disclosure over the org skill library so an
	// agent mid-run can discover a skill and pull its body on demand. Agent-authored skills
	// (skill_propose → human approval, mirroring plan_propose) are a documented fast-follow, out of scope here.
	{
		"name": "skill_list",
		"description": "Discover your org's ENABLED skills — a cheap index of name + one-line description + " +
			"version, WITHOUT the bodies. Call this when you realize a task might have a reusable org skill " +
			"(e.g. deploying, a house code style) so you can then skill_fetch the one you need. Pass an " +
			"optional 'filter' to substring-match names/descriptions.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filter": map[string]any{"type": "string", "description": "Optional case-insensitive substring to match against skill name/description."},
			},
		},
	},
	{
		"name": "skill_fetch",
		"description": "Fetch ONE org skill's full SKILL.md body (frontmatter + markdown instructions) by name. " +
			"Use skill_list first to find the exact name. This is the 'I need the deploy skill right now' " +
			"pull: read the returned body and follow it. Read-only — it does not install or modify anything.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "The skill's slug name (lowercase, from skill_list), e.g. \"deploy\"."},
			},
			"required": []string{"name"},
		},
	},
}

func (s *mcpServer) handleToolCall(enc *json.Encoder, req rpcReq) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)
	switch p.Name {
	case "read_channel":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		msgs, err := s.pc.Recent(ctx, s.name)
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not read the channel: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, formatChannel(msgs), false)
	case "read_transcript":
		md, err := s.pc.Transcript()
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not read the transcript: %v", err), true)
			return
		}
		if strings.TrimSpace(md) == "" {
			md = "(this party has no messages yet)"
		}
		s.toolResult(enc, req.ID, md, false)
	case "post":
		var a struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		if strings.TrimSpace(a.Message) == "" {
			s.toolResult(enc, req.ID, "post needs a 'message'.", true)
			return
		}
		if _, err := s.pc.Post(s.name, a.Message, "msg"); err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not post to the channel: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, "Posted to the channel.", false)
	case "who":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		msgs, err := s.pc.Recent(ctx, s.name)
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not read the channel: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, formatWho(msgs, s.name), false)
	case "help":
		s.toolResult(enc, req.ID, partylineHelpDoc, false)
	case "read_doc":
		body, ver, err := s.pc.GetDoc()
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not read the doc: %v", err), true)
			return
		}
		if strings.TrimSpace(body) == "" {
			body = "(the working doc is currently empty)"
		}
		s.toolResult(enc, req.ID, fmt.Sprintf("Shared working doc — version %d:\n\n%s", ver, body), false)
	case "propose_edit":
		var a struct {
			Section string `json:"section"`
			NewBody string `json:"new_body"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		if strings.TrimSpace(a.Section) == "" || strings.TrimSpace(a.NewBody) == "" {
			s.toolResult(enc, req.ID, "propose_edit needs both 'section' and 'new_body'.", true)
			return
		}
		if _, err := s.pc.ProposeEdit(s.name, a.Section, a.NewBody); err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not propose the edit: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, fmt.Sprintf("Proposed an edit to %q. A human will approve or reject it before it merges — don't assume it's applied yet.", a.Section), false)
	case "ask_human":
		var a struct {
			Question string `json:"question"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		if strings.TrimSpace(a.Question) == "" {
			s.toolResult(enc, req.ID, "ask_human needs a 'question'.", true)
			return
		}
		if _, err := s.pc.Post(s.name, a.Question, "ask"); err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not post the question: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, "Posted the question for a human.", false)
	case "propose_fix":
		var a struct {
			Task       string `json:"task"`
			Acceptance string `json:"acceptance"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		if strings.TrimSpace(a.Task) == "" || strings.TrimSpace(a.Acceptance) == "" {
			s.toolResult(enc, req.ID, "propose_fix needs both 'task' and 'acceptance'.", true)
			return
		}
		// The proposal rides as a `run_proposal` message whose body is JSON — the web renders an
		// approval card and, on a human's approval, enqueues a gated run (merge_policy=pr) that opens
		// a PR. Nothing runs until a human approves; the agent must not assume it's applied.
		body, _ := json.Marshal(map[string]string{"task": strings.TrimSpace(a.Task), "acceptance": strings.TrimSpace(a.Acceptance)})
		if _, err := s.pc.Post(s.name, string(body), "run_proposal"); err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not post the proposal: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, "Proposed a fix. A human will approve it to open a PR — don't assume it's running yet; wait for their decision.", false)
	case "plan_read":
		tree, err := s.pc.PlanRead()
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not read the plan: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, formatPlanTree(tree), false)
	case "capabilities":
		m, err := s.pc.Capabilities()
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not read your capabilities: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, m, false)
	case "backlog_read":
		b, err := s.pc.Backlog()
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not read the backlog: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, formatBacklog(b), false)
	case "plan_upsert":
		s.handlePlanUpsert(enc, req.ID, p.Arguments)
	case "plan_move":
		var a struct {
			ID       string   `json:"id"`
			ParentID *string  `json:"parent_id"`
			Rank     *float64 `json:"rank"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		if strings.TrimSpace(a.ID) == "" {
			s.toolResult(enc, req.ID, "plan_move needs an 'id'.", true)
			return
		}
		if err := s.pc.PlanMove(a.ID, a.ParentID, a.Rank); err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not move the plan item: %v", err), true)
			return
		}
		if a.ParentID != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("Moved plan item %s under %s.", a.ID, *a.ParentID), false)
			return
		}
		s.toolResult(enc, req.ID, fmt.Sprintf("Moved plan item %s to the top level.", a.ID), false)
	case "plan_propose":
		var a struct {
			Action string `json:"action"`
			ItemID string `json:"item_id"`
			Note   string `json:"note"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		if a.Action != "promote" && a.Action != "archive" {
			s.toolResult(enc, req.ID, `plan_propose 'action' must be "promote" or "archive".`, true)
			return
		}
		if strings.TrimSpace(a.ItemID) == "" {
			s.toolResult(enc, req.ID, "plan_propose needs an 'item_id'.", true)
			return
		}
		if err := s.pc.PlanPropose(a.Action, a.ItemID, strings.TrimSpace(a.Note)); err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not send the proposal: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, fmt.Sprintf("Proposed to %s item %s. A human will approve or reject it — nothing changes until they decide.", a.Action, a.ItemID), false)
	case "skill_list":
		var a struct {
			Filter string `json:"filter"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		c := skillClient()
		if strings.TrimSpace(c.Token) == "" {
			s.toolResult(enc, req.ID, "no partyline login on this machine — run `ptln login` so I can read your org's skills.", true)
			return
		}
		skills, err := c.ListSkills()
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not list org skills: %v", err), true)
			return
		}
		s.toolResult(enc, req.ID, formatSkillList(skills, strings.TrimSpace(a.Filter)), false)
	case "skill_fetch":
		var a struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		name := strings.TrimSpace(a.Name)
		if name == "" {
			s.toolResult(enc, req.ID, "skill_fetch needs a 'name'.", true)
			return
		}
		// Path/injection boundary: the name becomes an API path segment (/skills/<name>) and,
		// server-side, a filesystem path. Validate the slug shape BEFORE it's used — reject ../,
		// uppercase, slashes, empty. (Same canonical rule as skill materialization.)
		if !gitwt.ValidSkillName(name) {
			s.toolResult(enc, req.ID, fmt.Sprintf("invalid skill name %q — must be a lowercase slug matching ^[a-z0-9][a-z0-9-]{0,38}$.", name), true)
			return
		}
		c := skillClient()
		if strings.TrimSpace(c.Token) == "" {
			s.toolResult(enc, req.ID, "no partyline login on this machine — run `ptln login` so I can read your org's skills.", true)
			return
		}
		d, err := c.GetSkill(name)
		if err != nil {
			s.toolResult(enc, req.ID, fmt.Sprintf("could not fetch skill %q: %v", name, err), true)
			return
		}
		body := d.Body
		if strings.TrimSpace(body) == "" {
			body = "(this skill has an empty body)"
		}
		s.toolResult(enc, req.ID, body, false)
	default:
		s.toolResult(enc, req.ID, "unknown tool: "+p.Name, true)
	}
}

// planCreateKeys / planPatchKeys whitelist what plan_upsert forwards to the API (contract
// bodies for POST /plan/items and PATCH /plan/items/<id>). kind and parent_id are
// create-only: on edit, kind is immutable and reparenting is plan_move's job.
var planCreateKeys = []string{"kind", "title", "document", "acceptance_criteria", "readiness", "readiness_note", "parent_id"}
var planPatchKeys = []string{"title", "document", "acceptance_criteria", "readiness", "readiness_note"}

// handlePlanUpsert routes plan_upsert: `id` present → PATCH the item; absent → create one.
// Validation short-circuits before any API call (matching the other tools): create needs
// kind+title, and any readiness change must carry a readiness_note.
func (s *mcpServer) handlePlanUpsert(enc *json.Encoder, id json.RawMessage, arguments json.RawMessage) {
	var args map[string]any
	_ = json.Unmarshal(arguments, &args)
	itemID, _ := args["id"].(string)
	keys := planCreateKeys
	if strings.TrimSpace(itemID) != "" {
		keys = planPatchKeys
	}
	fields := map[string]any{}
	for _, k := range keys {
		if v, ok := args[k]; ok && v != nil {
			fields[k] = v
		}
	}
	if _, ok := fields["readiness"]; ok {
		if note, _ := fields["readiness_note"].(string); strings.TrimSpace(note) == "" {
			s.toolResult(enc, id, "changing readiness requires a one-line 'readiness_note' explaining why.", true)
			return
		}
	}
	if strings.TrimSpace(itemID) == "" { // create
		kind, _ := fields["kind"].(string)
		title, _ := fields["title"].(string)
		if strings.TrimSpace(kind) == "" || strings.TrimSpace(title) == "" {
			s.toolResult(enc, id, "plan_upsert needs 'kind' and 'title' to create an item (or an 'id' to edit one).", true)
			return
		}
		newID, err := s.pc.PlanCreateItem(fields)
		if err != nil {
			s.toolResult(enc, id, fmt.Sprintf("could not create the plan item: %v", err), true)
			return
		}
		s.toolResult(enc, id, fmt.Sprintf("Created plan item %s as a DRAFT. A human decides if it's promoted — use plan_propose to suggest that.", newID), false)
		return
	}
	if _, ok := args["parent_id"]; ok { // edit: reparenting is a move, not a patch
		s.toolResult(enc, id, "plan_upsert can't change 'parent_id' on an existing item — use plan_move.", true)
		return
	}
	if len(fields) == 0 {
		s.toolResult(enc, id, "plan_upsert edit needs at least one field to change (title, document, acceptance_criteria, or readiness + readiness_note).", true)
		return
	}
	if err := s.pc.PlanPatchItem(strings.TrimSpace(itemID), fields); err != nil {
		s.toolResult(enc, id, fmt.Sprintf("could not update the plan item: %v", err), true)
		return
	}
	s.toolResult(enc, id, fmt.Sprintf("Updated plan item %s.", strings.TrimSpace(itemID)), false)
}

// skillClient returns a USER-token client for the org skill library. This is deliberately NOT the
// party token (PartyClient): the skills endpoints (/api/v1/skills, /api/v1/skills/<name>) are
// requireUser + RLS, and the party token only resolves an open party (partyByToken), never a user —
// so it 401s there. There is no party-scoped skills read endpoint, and we do NOT add one in this
// slice. Instead we use the machine's logged-in user token (~/.partyline/token via api.New), which is
// what every other user-scoped ptln command uses and is present in the normal interactive case (the
// human driving the party is logged in locally). Caveat: skills are scoped to THAT user's org, which
// normally equals the party's org but can differ for a cross-org joiner or a headless daemon with no
// local login — those get a clear "run ptln login" message rather than another org's skills.
func skillClient() *api.Client { return api.New() }

// formatSkillList renders the ENABLED org skills for the model: name + version + one-line description,
// no bodies (progressive-disclosure discovery — the agent skill_fetches the one it wants).
func formatSkillList(skills []api.SkillMeta, filter string) string {
	var b strings.Builder
	n := 0
	lf := strings.ToLower(filter)
	for _, sk := range skills {
		if !sk.Enabled {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(sk.Name+" "+sk.Description), lf) {
			continue
		}
		fmt.Fprintf(&b, "\n- %s (v%d): %s", sk.Name, sk.Version, sk.Description)
		n++
	}
	if n == 0 {
		if filter != "" {
			return fmt.Sprintf("No enabled org skills match %q.", filter)
		}
		return "Your org has no enabled skills yet."
	}
	return fmt.Sprintf("Enabled org skills (%d) — fetch a body with skill_fetch <name>:\n%s", n, b.String())
}

// formatPlanTree renders the planning tree for the model: one indented line per item with
// the id (needed for plan_upsert/plan_move/plan_propose), status, readiness and note.
func formatPlanTree(t *api.PlanTree) string {
	var b strings.Builder
	title := t.ThreadTitle
	if title == "" {
		title = t.ThreadID
	}
	fmt.Fprintf(&b, "Plan for %q (thread %s):\n", title, t.ThreadID)
	if len(t.Tree) == 0 {
		b.WriteString("(the plan has no items yet — plan_upsert can draft the first one)")
		return b.String()
	}
	writePlanItems(&b, t.Tree, 0)
	return strings.TrimRight(b.String(), "\n")
}

func writePlanItems(b *strings.Builder, items []api.PlanItem, depth int) {
	for _, it := range items {
		b.WriteString(strings.Repeat("  ", depth))
		fmt.Fprintf(b, "- [%s] %s (id=%s, status=%s", it.Kind, it.Title, it.ID, it.Status)
		if it.Readiness != nil {
			fmt.Fprintf(b, ", readiness=%v", it.Readiness)
		}
		b.WriteString(")")
		if strings.TrimSpace(it.ReadinessNote) != "" {
			b.WriteString(" — " + strings.TrimSpace(it.ReadinessNote))
		}
		if it.HasRun {
			b.WriteString(" [has run]")
		}
		b.WriteString("\n")
		writePlanItems(b, it.Children, depth+1)
	}
}

// formatChannel renders the channel backlog for the model: each message as "who: body",
// oldest first. Skips status spam; flags questions that want a human.
func formatChannel(msgs []api.PartyMsg) string {
	var b strings.Builder
	n := 0
	for _, m := range msgs {
		if m.Kind == "status" || strings.TrimSpace(m.Body) == "" {
			continue
		}
		tag := ""
		if m.Kind == "ask" {
			tag = " [needs a human]"
		}
		b.WriteString(fmt.Sprintf("\n%s%s: %s", senderLabel(m.Sender), tag, strings.TrimSpace(m.Body)))
		n++
	}
	if n == 0 {
		return "The channel has no messages yet — you can open the conversation with post."
	}
	return fmt.Sprintf("Recent messages (%d), oldest first:\n%s", n, b.String())
}

// formatWho lists distinct recent participants, splitting humans from agents.
func formatWho(msgs []api.PartyMsg, me string) string {
	seen := map[string]bool{}
	var humans, agents []string
	for _, m := range msgs {
		if seen[m.Sender] {
			continue
		}
		seen[m.Sender] = true
		switch {
		case strings.HasPrefix(m.Sender, "agent:"):
			n := strings.TrimPrefix(m.Sender, "agent:")
			if n == me {
				n += " (you)"
			}
			agents = append(agents, n)
		case strings.HasPrefix(m.Sender, "user:"):
			humans = append(humans, strings.TrimPrefix(m.Sender, "user:"))
		}
	}
	if len(humans) == 0 && len(agents) == 0 {
		return "No one has spoken in the channel yet."
	}
	var b strings.Builder
	b.WriteString("Recently active in the party:")
	if len(humans) > 0 {
		b.WriteString("\nHumans: " + strings.Join(humans, ", "))
	}
	if len(agents) > 0 {
		b.WriteString("\nAgents: @" + strings.Join(agents, ", @"))
	}
	b.WriteString("\n\n(Address someone with @name.)")
	return b.String()
}

func senderLabel(s string) string {
	if n := strings.TrimPrefix(s, "agent:"); n != s {
		return n + " (agent)"
	}
	if n := strings.TrimPrefix(s, "user:"); n != s {
		return n
	}
	return s
}

// toolResult sends an MCP tools/call result: a single text content block, with isError
// set when the tool failed (the engine surfaces it to the model rather than aborting).
func (s *mcpServer) toolResult(enc *json.Encoder, id json.RawMessage, text string, isErr bool) {
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if isErr {
		res["isError"] = true
	}
	s.reply(enc, id, res)
}

func (s *mcpServer) reply(enc *json.Encoder, id json.RawMessage, result interface{}) {
	_ = enc.Encode(rpcResp{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *mcpServer) replyErr(enc *json.Encoder, id json.RawMessage, code int, msg string) {
	_ = enc.Encode(rpcResp{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// formatBacklog renders the team's board for a model: grouped, titled, counted. Plain text rather
// than JSON because the agent reads this to REASON about what to propose next, not to act on ids —
// and ids it can't act on are context it shouldn't be paying for.
func formatBacklog(b *api.Backlog) string {
	if b == nil {
		return "The backlog came back empty."
	}
	var sb strings.Builder
	section := func(title string, n int, body func()) {
		if n == 0 {
			return
		}
		fmt.Fprintf(&sb, "%s (%d)\n", title, n)
		body()
		sb.WriteString("\n")
	}
	// Needs-attention leads deliberately: a run awaiting a human is the most decision-relevant thing
	// on the board, and it's the one an agent would otherwise re-propose from scratch.
	section("NEEDS ATTENTION — a human is the blocker", len(b.NeedsAttention), func() {
		for _, r := range b.NeedsAttention {
			if r.Reason != "" {
				fmt.Fprintf(&sb, "  · [%s] %s — %s\n", r.Status, r.Title, r.Reason)
			} else {
				fmt.Fprintf(&sb, "  · [%s] %s\n", r.Status, r.Title)
			}
		}
	})
	section("BUILDING now", len(b.Building), func() {
		for _, r := range b.Building {
			fmt.Fprintf(&sb, "  · [%s] %s\n", r.Status, r.Title)
		}
	})
	section("QUEUED — in the order they will run", len(b.Queued), func() {
		for i, r := range b.Queued {
			fmt.Fprintf(&sb, "  %d. %s\n", i+1, r.Title)
		}
	})
	section("SHIPPED recently", len(b.RecentlyShipped), func() {
		for _, r := range b.RecentlyShipped {
			fmt.Fprintf(&sb, "  · %s\n", r.Title)
		}
	})
	if sb.Len() == 0 {
		return "The Build backlog is empty — nothing queued, building, or awaiting approval."
	}
	if len(b.Totals) > 0 {
		sb.WriteString("Totals by status: ")
		first := true
		for _, k := range []string{"queued", "accepted", "running", "paused", "needs_approval", "failed", "done", "killed"} {
			if n, ok := b.Totals[k]; ok && n > 0 {
				if !first {
					sb.WriteString(" · ")
				}
				fmt.Fprintf(&sb, "%s %d", k, n)
				first = false
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
