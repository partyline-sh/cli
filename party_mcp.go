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
		s.toolResult(enc, req.ID, "Posted your question to the channel for a human to answer.", false)
	default:
		s.toolResult(enc, req.ID, "unknown tool: "+p.Name, true)
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
