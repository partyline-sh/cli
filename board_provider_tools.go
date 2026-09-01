package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// board_provider_tools.go — calling an MCP server's TOOLS, rather than reading a resource it
// declared for us.
//
// The resource contract (partyline://board) asks a server to grow a partyline-shaped surface. That
// is the right deal for a server someone writes for this, and the wrong one for a shared production
// server with four other consumers — an Odoo, Jira or Linear MCP that already exists and that
// nobody should be editing to suit a board. For those, partyline asks the questions instead, using
// tools the server already answers, and assembles the board on this side.
//
// Both are legitimate and both land on boardSource. What changes is only who holds the knowledge of
// how a board is assembled.

// mcpToolResult is what tools/call returns. Servers overwhelmingly answer with a text block holding
// JSON rather than a structured result, so the text is what gets parsed.
type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// text pulls the JSON document out of a tool result.
func (r mcpToolResult) text() (string, error) {
	if len(r.StructuredContent) > 0 && string(r.StructuredContent) != "null" {
		return string(r.StructuredContent), nil
	}
	for _, c := range r.Content {
		if s := strings.TrimSpace(c.Text); s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("the tool returned nothing")
}

func decodeToolResult(raw json.RawMessage) (string, error) {
	var res mcpToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("tool result was not readable: %w", err)
	}
	body, err := res.text()
	if err != nil {
		return "", err
	}
	// isError carries the server's own failure as a normal result, so it has to be checked
	// explicitly — a tool that says "no such model" would otherwise parse as an empty board.
	if res.IsError {
		return "", fmt.Errorf("%s", firstLineOf(body))
	}
	return body, nil
}

// callTool runs one tool on the server behind this source, over whichever transport it uses.
func (p providerSource) callTool(name string, args map[string]any) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("no tool configured to call")
	}
	if p.url != "" {
		return newHTTPTransport(p.url, p.headers).callTool(name, args)
	}

	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	srv, err := startMCP(ctx, p.command, p.args, p.env)
	if err != nil {
		return "", err
	}
	defer srv.close()

	if _, err := srv.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ptln-board", "version": version},
	}); err != nil {
		return "", err
	}
	raw, err := srv.call("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	return decodeToolResult(raw)
}

func (h *httpTransport) callTool(name string, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()

	if _, err := h.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ptln-board", "version": version},
	}); err != nil {
		return "", err
	}
	raw, err := h.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	return decodeToolResult(raw)
}

// rawListTools returns the server's tools/list result, over whichever transport it uses.
func (p providerSource) rawListTools() (json.RawMessage, error) {
	init := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ptln-board", "version": version},
	}
	if p.url != "" {
		h := newHTTPTransport(p.url, p.headers)
		ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
		defer cancel()
		if _, err := h.call(ctx, "initialize", init); err != nil {
			return nil, err
		}
		return h.call(ctx, "tools/list", map[string]any{})
	}

	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	srv, err := startMCP(ctx, p.command, p.args, p.env)
	if err != nil {
		return nil, err
	}
	defer srv.close()
	if _, err := srv.call("initialize", init); err != nil {
		return nil, err
	}
	return srv.call("tools/list", map[string]any{})
}
