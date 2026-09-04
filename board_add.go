package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// board_add.go — connecting another tracker's board, from inside the board.
//
// The shape of this is the point. partyline cannot look at an arbitrary MCP server and deduce how
// to build a board — that is inference, and inference at render time gives you a board that draws
// differently tomorrow. So the inference happens ONCE, here, when you add the server: partyline
// asks what tools it has, decides how it would read a board from it, and writes that decision into
// ~/.partyline/mcp.json. Every load afterwards is a deterministic replay of saved config.
//
// The same reason the project-setup agent works: a person confirms the hard part once, and what
// survives is configuration rather than a guess repeated forever.

// beginAddBoard lists the catalogued MCP servers that are not boards yet.
func (m *boardModel) beginAddBoard(c *api.Client) bool {
	cat := loadMCPCatalog()
	var names []string
	for name, def := range cat {
		if def.Board.Enabled {
			continue // already a board — it is in the list you just came from
		}
		if strings.TrimSpace(def.URL) == "" && strings.TrimSpace(def.Command) == "" {
			continue // an entry describing neither transport cannot be called at all
		}
		names = append(names, name)
	}
	sort.Strings(names)

	if len(names) == 0 {
		m.setToast("no other MCP servers in ~/.partyline/mcp.json — add one with ctrl-\\ m", false)
		return false
	}

	items := make([]pickerItem, 0, len(names))
	for _, n := range names {
		items = append(items, pickerItem{Label: n, Note: mcpWhere(cat[n]), Value: n})
	}
	m.openOverlay(&pickerOverlay{
		heading: "add a board — which MCP server?",
		items:   items,
		onPick: func(m *boardModel, c *api.Client, v pickerItem) bool {
			return m.connectBoard(v.Value, cat[v.Value])
		},
	})
	return false
}

// mcpWhere is the one-line "where does this server live", for the picker's note column.
func mcpWhere(d mcpDef) string {
	if u := strings.TrimSpace(d.URL); u != "" {
		return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	}
	return d.Command
}

// connectBoard asks the server what it can do, decides how to read a board from it, and saves that.
func (m *boardModel) connectBoard(name string, def mcpDef) bool {
	p := providerSource{
		name: name, url: strings.TrimSpace(def.URL), headers: def.Headers,
		command: def.Command, args: def.Args, env: def.Env,
	}

	cfg, how, err := detectBoardKind(p)
	if err != nil {
		m.setToast(name+": "+err.Error(), true)
		return false
	}
	if err := saveBoardConfig(name, cfg); err != nil {
		m.setToast("could not write ~/.partyline/mcp.json: "+err.Error(), true)
		return false
	}

	// Re-read the catalog rather than appending to the slice: the new board then arrives by exactly
	// the same path it will take on every future launch, so a mistake here shows up now.
	m.sources = append([]boardSource{partylineSource{c: m.client}}, discoverBoardProviders()...)
	for i, s := range m.sources {
		if s.Name() == name {
			m.src = i
			m.scope = loadBoardScope(name)
			m.data, m.focusID, m.focusChainID = nil, "", ""
			m.col = 0
			m.setPendingToast(how)
			return true
		}
	}
	m.setToast("saved, but "+name+" did not come back as a board", true)
	return false
}

// detectBoardKind works out how this server can serve a board, preferring what it declares over
// what we can infer.
func detectBoardKind(p providerSource) (boardConfig, string, error) {
	// A server that implements the contract has said what it is; nothing to infer.
	if _, err := p.read(boardResourceURI); err == nil {
		return boardConfig{Enabled: true}, p.name + " serves a partyline board directly", nil
	}

	tools, err := p.listTools()
	if err != nil {
		return boardConfig{}, "", fmt.Errorf("could not reach it: %w", err)
	}
	if len(tools) == 0 {
		return boardConfig{}, "", fmt.Errorf("it offers no tools and no partyline board")
	}

	has := map[string]bool{}
	for _, t := range tools {
		has[t] = true
	}
	// Odoo is recognised by its generic query tool, not by its name. That tool IS the capability —
	// with it, stock Odoo models are enough to build a board; without it, a server named "odoo"
	// still could not answer the questions.
	for _, q := range []string{"odoo_search_read", "search_read"} {
		if has[q] {
			cfg := boardConfig{Enabled: true, Kind: "odoo", Opts: map[string]string{"query_tool": q}}
			if !has["list_projects"] {
				cfg.Opts["scopes_tool"] = "-" // fall back to querying project.project directly
			}
			return cfg, "added " + p.name + " — press p to pick a project", nil
		}
	}
	return boardConfig{}, "", fmt.Errorf(
		"it has no tool this ptln can build a board from (looked for a generic Odoo search_read, "+
			"or the %s resource)", boardResourceURI)
}

// listTools names what a server offers.
func (p providerSource) listTools() ([]string, error) {
	raw, err := p.rawListTools()
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("its tool list was not readable: %w", err)
	}
	names := make([]string, 0, len(out.Tools))
	for _, t := range out.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}

// saveBoardConfig writes one server's board settings back, leaving every other entry byte-for-byte
// as it was. The catalog is a file people hand-edit and that the ctrl-\ m menu also writes;
// rewriting the whole thing from a parsed struct would quietly drop anything this binary does not
// model.
func saveBoardConfig(name string, cfg boardConfig) error {
	raw, err := readCatalogRaw()
	if err != nil {
		return err
	}
	servers, _ := raw["servers"].(map[string]any)
	if servers == nil {
		return fmt.Errorf("no servers in the catalog")
	}
	entry, _ := servers[name].(map[string]any)
	if entry == nil {
		return fmt.Errorf("%s is not in the catalog", name)
	}
	b, err := cfg.MarshalJSON()
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	entry["board"] = v
	servers[name] = entry
	raw["servers"] = servers
	return writeCatalogRaw(raw)
}
