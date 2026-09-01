package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/api"
)

// board_provider.go — reading a board out of an MCP server.
//
// partyline has always been an MCP SERVER; this is the first place it is a client. The direction
// matters: a provider is a separate process on the operator's side that holds its own tracker
// credentials and answers two questions. partyline learns no tracker API and stores no tracker
// token — the constraint from docs/plans/board-providers.md, and from the import route that
// predates it.
//
// Why MCP rather than a plugin format of our own: it is the mechanism already in the product (a
// catalog, per-project grants, a heartbeat that reports names without paths or secrets), anyone can
// write one in any language, and it is a subprocess rather than code loaded into the process that
// holds the account token. Go's own plugin package was never a candidate — exact toolchain matching,
// no Windows.

// boardResourceURI is the resource a server must expose to be a board provider.
//
// A RESOURCE rather than a tool, deliberately: resources are MCP's primitive for "here is data",
// tools are for "do a thing". A board is data.
const boardResourceURI = "partyline://board"

// boardScopesURI is the optional companion listing what can be selected. A server without it has
// exactly one board and the scope picker is not offered.
const boardScopesURI = "partyline://board/scopes"

// providerTimeout bounds a provider call. A tracker behind a slow VPN must not hang the board; the
// call fails, the status line says so, and the previous board stays on screen.
const providerTimeout = 20 * time.Second

// ── the MCP client ───────────────────────────────────────────────────────────────────────────────

// mcpStdio is one spawned MCP server, spoken to over stdio.
//
// Deliberately short-lived: spawned per read, torn down after. A resident subprocess per provider
// would be faster and would also mean partyline keeps somebody's tracker connection open all day
// for a board that only refreshes when you press g. Not worth it.
type mcpStdio struct {
	cmd  *exec.Cmd
	in   *bufio.Writer
	out  *bufio.Reader
	next int
	mu   sync.Mutex
}

func startMCP(ctx context.Context, command string, args []string, env map[string]string) (*mcpStdio, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = nil // a provider's chatter is not ours to render

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &mcpStdio{cmd: cmd, in: bufio.NewWriter(stdin), out: bufio.NewReader(stdout), next: 1}, nil
}

func (m *mcpStdio) close() {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_ = m.cmd.Wait()
	}
}

// call sends one JSON-RPC request and reads until the matching response.
//
// Notifications and any other id arrive interleaved and are skipped rather than treated as the
// answer — a server that logs progress must not break the read.
func (m *mcpStdio) call(method string, params any) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := m.next
	m.next++
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := m.in.Write(append(body, '\n')); err != nil {
		return nil, err
	}
	if err := m.in.Flush(); err != nil {
		return nil, err
	}

	for {
		line, err := m.out.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("provider closed the connection: %w", err)
		}
		var resp struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &resp) != nil || resp.ID == nil || *resp.ID != id {
			continue // a notification, a log line, or somebody else's id
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s", resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// readResource performs the handshake and reads one resource's text.
func (m *mcpStdio) readResource(uri string) ([]byte, error) {
	if _, err := m.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ptln-board", "version": version},
	}); err != nil {
		return nil, err
	}
	raw, err := m.call("resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	return resourceText(raw, uri)
}

// ── the provider source ──────────────────────────────────────────────────────────────────────────

// providerSource is a board that lives behind an MCP server, over either transport.
//
// The catalog has always described two kinds — a stdio server (command+args+env) and an HTTP one
// (url+headers) — and a board provider can be either. A hosted server behind a bearer token has no
// subprocess to spawn at all, and requiring one would have meant inventing a local bridge for every
// such deployment.
type providerSource struct {
	name string

	// stdio
	command string
	args    []string
	env     map[string]string

	// http
	url     string
	headers map[string]string
}

func (p providerSource) Name() string { return p.name }

func (p providerSource) read(uri string) ([]byte, error) {
	if p.url != "" {
		return newHTTPTransport(p.url, p.headers).readResource(uri)
	}

	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()

	srv, err := startMCP(ctx, p.command, p.args, p.env)
	if err != nil {
		return nil, err
	}
	defer srv.close()
	return srv.readResource(uri)
}

func (p providerSource) Scopes() ([]boardScope, error) {
	body, err := p.read(boardScopesURI)
	if err != nil {
		return nil, nil // no scopes resource means one board — not an error worth reporting
	}
	var out struct {
		Scopes []boardScope `json:"scopes"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("its project list was not readable: %w", err)
	}
	for i := range out.Scopes {
		out.Scopes[i].Label = safeForeignText(out.Scopes[i].Label)
		out.Scopes[i].Note = safeForeignText(out.Scopes[i].Note)
		out.Scopes[i].ID = safeForeignText(out.Scopes[i].ID)
	}
	return out.Scopes, nil
}

// wireBoard is the payload shape a provider returns. Kept separate from boardData so a malformed
// provider cannot reach into the model — it is decoded here, validated, sanitized, and only then
// turned into something the board renders.
type wireBoard struct {
	Columns []struct {
		Key   string `json:"key"`
		Title string `json:"title"`
	} `json:"columns"`
	Cards []struct {
		ID       string `json:"id"`
		Column   string `json:"column"`
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		Detail   string `json:"detail"`
		URL      string `json:"url"`
		State    string `json:"state"`
		Urgent   bool   `json:"urgent"`

		// Everything that does not fit on a tile — rendered in the detail pane.
		Fields []struct {
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"fields"`
		Body string `json:"body"`
	} `json:"cards"`
	ReadAt string `json:"read_at"`
	Scope  string `json:"scope"`
}

func (p providerSource) Load(scopeID string) (*boardData, error) {
	uri := boardResourceURI
	if scopeID != "" {
		uri += "?scope=" + scopeID
	}
	body, err := p.read(uri)
	if err != nil {
		return nil, err
	}

	var w wireBoard
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("returned something this board cannot read: %w", err)
	}
	if len(w.Columns) == 0 {
		return nil, fmt.Errorf("returned no columns")
	}

	d := &boardData{
		ByColumn: map[api.BoardColumn][]api.BoardCard{},
		Source:   p.name,
		Scope:    w.Scope,
		Live:     false, // foreign boards never poll — see pollBoard
		ReadAt:   time.Now(),
	}
	if t, err := time.Parse(time.RFC3339, w.ReadAt); err == nil {
		d.ReadAt = t // the provider's own read time beats ours when it offers one
	}

	known := map[string]bool{}
	for _, col := range w.Columns {
		key := api.BoardColumn(safeForeignText(col.Key))
		if key == "" || known[string(key)] {
			continue
		}
		known[string(key)] = true
		title := safeForeignText(col.Title)
		if title == "" {
			title = string(key)
		}
		d.Columns = append(d.Columns, boardColumn{Key: key, Title: title})
		d.ByColumn[key] = nil
	}
	if len(d.Columns) == 0 {
		return nil, fmt.Errorf("returned no usable columns")
	}

	for _, c := range w.Cards {
		key := api.BoardColumn(safeForeignText(c.Column))
		if !known[string(key)] {
			continue // a card in a column the provider never declared has nowhere to go
		}
		card := api.BoardCard{
			ID:         c.ID,
			Task:       c.Title,
			Title:      c.Subtitle,
			Detail:     c.Detail,
			SourceURL:  c.URL,
			StateLabel: c.State,
			Attention:  c.Urgent,
			Body:       c.Body,
			Column:     key,
			Foreign:    true,
		}
		for _, f := range c.Fields {
			card.Fields = append(card.Fields, api.BoardCardField{Label: f.Label, Value: f.Value})
		}
		d.ByColumn[key] = append(d.ByColumn[key], card)
	}

	sanitizeForeignBoard(d) // every string, once, at the boundary
	return d, nil
}

// ── discovery ────────────────────────────────────────────────────────────────────────────────────

// discoverBoardProviders finds the MCP servers this machine has that declare themselves boards.
//
// Declared, not probed: spawning every catalogued MCP server on every board launch to ask whether it
// happens to be a board would be slow and rude to servers that are not. An entry opts in with
// "board": true.
func discoverBoardProviders() []boardSource {
	var out []boardSource
	for name, srv := range loadMCPCatalog() {
		if !srv.Board {
			continue
		}
		// EITHER transport. Skipping an entry that has a url but no command is how an HTTP board
		// provider used to vanish without a word — configured correctly, marked as a board, and
		// simply absent from the switcher.
		switch {
		case strings.TrimSpace(srv.URL) != "":
			out = append(out, providerSource{name: name, url: strings.TrimSpace(srv.URL), headers: srv.Headers})
		case strings.TrimSpace(srv.Command) != "":
			out = append(out, providerSource{name: name, command: srv.Command, args: srv.Args, env: srv.Env})
		}
	}
	return out
}
