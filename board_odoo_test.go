package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// board_odoo_test.go — the Odoo board, built from a server's ordinary tools.
//
// Weighted toward Odoo's two encodings, because both LOOK like working code when they are wrong:
// an empty field arrives as `false` rather than null, and a many2one arrives as [id, name] rather
// than a scalar. Getting either wrong produces a board full of the word "false", or one where every
// card sits in a column called nothing.

// odooServer stands up an MCP server that answers tools/call the way the real one does: a JSON
// document inside a text content block.
func odooServer(t *testing.T, reply func(tool string, args map[string]any) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "odoo_search_read"}, map[string]any{"name": "list_projects"},
			}}
		case "tools/call":
			var p struct {
				Name string         `json:"name"`
				Args map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			doc, _ := json.Marshal(reply(p.Name, p.Args))
			result = map[string]any{"content": []any{
				map[string]any{"type": "text", "text": string(doc)},
			}}
		}
		env, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(env)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fleetBoard is the shape the live ACR server returns, kept faithful to what was observed: stages
// carrying a duplicated-set suffix, a many2one stage, and `false` where a value is absent.
func fleetBoard(tool string, args map[string]any) any {
	if tool == "list_projects" {
		return map[string]any{"projects": []any{
			map[string]any{"id": 528, "name": "Fleet Manager", "task_count": 34, "active": true},
			map[string]any{"id": 495, "name": "darcytest", "task_count": 0, "active": true},
			map[string]any{"id": 9, "name": "Retired", "task_count": 3, "active": false},
		}}
	}
	switch args["model"] {
	case "project.project.stage":
		return map[string]any{"records": []any{
			map[string]any{"id": 1, "name": "New", "sequence": 0},
			map[string]any{"id": 9, "name": "Development", "sequence": 3},
			map[string]any{"id": 5, "name": "Complete", "sequence": 6},
		}}
	case "project.task.type":
		return map[string]any{"records": []any{
			map[string]any{"id": 204, "name": "New_1", "sequence": 1},
			map[string]any{"id": 214, "name": "Complete_1", "sequence": 6},
		}}
	case "project.task":
		if args["offset"] != nil {
			return map[string]any{"records": []any{}}
		}
		return map[string]any{"records": []any{
			map[string]any{
				"id": 28194, "name": "Store enrolment at scale",
				"stage_id": []any{float64(204), "New_1"},
				"user_ids": []any{float64(1308)},
				// every one of these is `false` on a real record, not null and not ""
				"partner_id": false, "date_deadline": false, "priority": "0",
				"write_date":  "2026-08-24 14:35:35",
				"description": "<p>Roll out to <b>100</b> stores.</p><p>Blocked on certs.</p>",
			},
			map[string]any{
				"id": 28185, "name": "Enforce certificate revocation",
				"stage_id": []any{float64(214), "Complete_1"},
				"user_ids": []any{}, "partner_id": []any{float64(77), "Rouses Markets"},
				"date_deadline": "2026-09-15", "priority": "1",
				"write_date": "2026-08-28 14:09:32", "description": false,
			},
			map[string]any{ // a stage this project does not list — it has nowhere to sit
				"id": 999, "name": "Orphan", "stage_id": []any{float64(777), "Elsewhere"},
				"user_ids": []any{}, "partner_id": false, "description": false,
			},
		}}
	case "res.users":
		return map[string]any{"records": []any{
			map[string]any{"id": 1308, "name": "Darcy Reno"},
		}}
	case "mail.message":
		return map[string]any{"records": []any{
			map[string]any{"res_id": 28194, "body": "<p>Waiting on the CA.</p>",
				"author_id": []any{float64(2), "Sam"}, "date": "2026-08-20 09:00:00"},
			map[string]any{"res_id": 28194, "body": "<p>Kicked off.</p>",
				"author_id": []any{float64(2), "Sam"}, "date": "2026-08-18 09:00:00"},
			map[string]any{"res_id": 28194, "body": false, // a bodiless field-change tracking row
				"author_id": []any{float64(2), "Sam"}, "date": "2026-08-19 09:00:00"},
		}}
	case "project.project":
		if args["offset"] != nil {
			return map[string]any{"records": []any{}}
		}
		// a scopeLabel lookup asks for one project by id; the board asks for all of them
		if fs, ok := args["fields"].([]any); ok && len(fs) == 1 {
			return map[string]any{"records": []any{map[string]any{"name": "Fleet Manager"}}}
		}
		return map[string]any{"records": []any{
			map[string]any{"id": 528, "name": "Fleet Manager", "stage_id": []any{float64(1), "New"},
				"task_count": float64(35), "user_id": []any{float64(1308), "Darcy Reno"},
				"partner_id": false, "description": false},
			map[string]any{"id": 354, "name": "Bugs - 2026", "stage_id": []any{float64(9), "Development"},
				"task_count": float64(145), "user_id": false,
				"partner_id": []any{float64(77), "Rouses Markets"}, "description": false},
			map[string]any{ // a project in a stage the kanban does not list
				"id": 999, "name": "Orphan", "stage_id": []any{float64(404), "Nowhere"},
				"task_count": float64(0), "user_id": false, "partner_id": false, "description": false},
		}}
	}
	return map[string]any{"records": []any{}}
}

func fleetSource(t *testing.T, opts map[string]string) odooSource {
	t.Helper()
	srv := odooServer(t, fleetBoard)
	return odooSource{
		p:   providerSource{name: "odoo", url: srv.URL},
		cfg: boardConfig{Enabled: true, Kind: "odoo", Opts: opts},
	}
}

func TestOdooBoardColumnsAreOdoosOwnStages(t *testing.T) {
	s := fleetSource(t, map[string]string{"strip_suffix": "_1"})
	d, err := s.Load("528")
	if err != nil {
		t.Fatal(err)
	}
	keys := d.Keys()
	if len(keys) != 2 {
		t.Fatalf("columns = %+v, want the project's two stages", d.Columns)
	}
	// Not partyline's five. A stage set squeezed into backlog/building/blocked/review/accepted
	// loses the distinction the board was opened for.
	for _, k := range keys {
		switch string(k) {
		case "backlog", "building", "blocked", "review", "accepted":
			t.Fatalf("an Odoo stage was mapped onto a partyline column: %q", k)
		}
	}
	if got := d.Title(keys[0]); got != "New" {
		t.Errorf("column title = %q — the configured suffix was not stripped", got)
	}
	if d.Live {
		t.Error("a foreign board must never poll on the live ticker")
	}
}

// The encoding that would otherwise fill the board with the word "false".
func TestOdooEmptyFieldsAreNotTheWordFalse(t *testing.T) {
	s := fleetSource(t, nil)
	d, err := s.Load("528")
	if err != nil {
		t.Fatal(err)
	}
	card, ok := d.Find("28194")
	if !ok {
		t.Fatal("the first task is missing")
	}
	for _, f := range card.Fields {
		if strings.Contains(strings.ToLower(f.Value), "false") {
			t.Errorf("field %q rendered Odoo's empty marker: %q", f.Label, f.Value)
		}
		if f.Label == "customer" || f.Label == "deadline" {
			t.Errorf("%q is unset on this task and must be omitted, not shown empty", f.Label)
		}
	}
	// priority "0" is Odoo's default that nobody ever sets; showing it implies a decision.
	for _, f := range card.Fields {
		if f.Label == "priority" {
			t.Errorf("the default priority must not be shown as one: %q", f.Value)
		}
	}
}

// A many2one is [id, name]. Read as a scalar it yields neither.
func TestOdooManyToOneGivesBothHalves(t *testing.T) {
	s := fleetSource(t, map[string]string{"strip_suffix": "_1"})
	d, err := s.Load("528")
	if err != nil {
		t.Fatal(err)
	}
	card, ok := d.Find("28185")
	if !ok {
		t.Fatal("the second task is missing")
	}
	if string(card.Column) != "214" {
		t.Errorf("column = %q, want the stage id from the pair", card.Column)
	}
	if card.StateLabel != "Complete" {
		t.Errorf("state = %q, want the stage name from the pair", card.StateLabel)
	}
	var customer string
	for _, f := range card.Fields {
		if f.Label == "customer" {
			customer = f.Value
		}
	}
	if customer != "Rouses Markets" {
		t.Errorf("customer = %q — the name half of the pair was lost", customer)
	}
}

// The assignee is a bare id list; a board that showed "1308" would be useless.
func TestOdooResolvesAssigneeNames(t *testing.T) {
	s := fleetSource(t, nil)
	d, _ := s.Load("528")
	card, _ := d.Find("28194")
	var who string
	for _, f := range card.Fields {
		if f.Label == "assignee" {
			who = f.Value
		}
	}
	if who != "Darcy Reno" {
		t.Errorf("assignee = %q, want the resolved name", who)
	}
}

// The whole reason the detail pane exists: not having to open Odoo.
func TestOdooBodyCarriesDescriptionAndChatter(t *testing.T) {
	s := fleetSource(t, nil)
	d, _ := s.Load("528")
	card, _ := d.Find("28194")

	if strings.Contains(card.Body, "<p>") || strings.Contains(card.Body, "<b>") {
		t.Errorf("HTML reached the pane: %q", card.Body)
	}
	for _, want := range []string{"Roll out to 100 stores", "Blocked on certs", "Kicked off", "Waiting on the CA"} {
		if !strings.Contains(card.Body, want) {
			t.Errorf("body is missing %q:\n%s", want, card.Body)
		}
	}
	// oldest first — a conversation read newest-first is not a conversation
	if strings.Index(card.Body, "Kicked off") > strings.Index(card.Body, "Waiting on the CA") {
		t.Error("chatter is in reverse order")
	}
	// Odoo logs a bodiless row for every field change; they would pad every card with blanks.
	if strings.Contains(card.Body, "\n\n\n") {
		t.Errorf("empty tracking rows were not dropped:\n%q", card.Body)
	}
}

// A card whose stage the project does not list has nowhere to go.
func TestOdooDropsCardsInUndeclaredStages(t *testing.T) {
	s := fleetSource(t, nil)
	d, _ := s.Load("528")
	if _, ok := d.Find("999"); ok {
		t.Error("a task in an unlisted stage was placed on the board anyway")
	}
}

// Scopes are the projects, with the count that tells you which ones are worth opening.
func TestOdooScopesSkipArchivedProjects(t *testing.T) {
	s := fleetSource(t, nil)
	scopes, err := s.Scopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 {
		t.Fatalf("scopes = %+v, want only the active projects", scopes)
	}
	if scopes[0].Note != "34 tasks" {
		t.Errorf("note = %q, want the task count", scopes[0].Note)
	}
	if scopes[1].Note != "empty" {
		t.Errorf("an empty project should say so, got %q", scopes[1].Note)
	}
}

// Adding a board infers the kind ONCE and writes it down; every later load replays that decision.
func TestDetectingAnOdooServerFromItsTools(t *testing.T) {
	srv := odooServer(t, fleetBoard)
	cfg, how, err := detectBoardKind(providerSource{name: "acr-odoo", url: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kind != "odoo" || !cfg.Enabled {
		t.Fatalf("cfg = %+v, want an enabled odoo board", cfg)
	}
	if cfg.Opts["query_tool"] != "odoo_search_read" {
		t.Errorf("the detected query tool must be recorded, got %+v", cfg.Opts)
	}
	if !strings.Contains(how, "acr-odoo") {
		t.Errorf("the confirmation should name the server, got %q", how)
	}
}

// A server with nothing a board can be built from must say so, not be added and then fail on load.
func TestDetectingRefusesAServerItCannotRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := map[string]any{}
		if req.Method == "tools/list" {
			result = map[string]any{"tools": []any{map[string]any{"name": "send_email"}}}
		}
		env, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = w.Write(env)
	}))
	defer srv.Close()

	if _, _, err := detectBoardKind(providerSource{name: "mailer", url: srv.URL}); err == nil {
		t.Fatal("a server with no board-shaped capability must be refused at add time")
	}
}

// "board": true has to keep meaning what it always meant, or every existing catalog breaks.
func TestBoardConfigAcceptsBothShapes(t *testing.T) {
	var flag boardConfig
	if err := json.Unmarshal([]byte(`true`), &flag); err != nil {
		t.Fatal(err)
	}
	if !flag.Enabled || flag.Kind != "" {
		t.Fatalf("bare true must mean the resource contract, got %+v", flag)
	}
	out, _ := flag.MarshalJSON()
	if string(out) != "true" {
		t.Errorf("the simple form must round-trip unchanged, got %s", out)
	}

	var obj boardConfig
	if err := json.Unmarshal([]byte(`{"kind":"odoo","opts":{"strip_suffix":"_1"},"poll_minutes":10}`), &obj); err != nil {
		t.Fatal(err)
	}
	if !obj.Enabled || obj.Kind != "odoo" || obj.Poll != 10 || obj.Opt("strip_suffix", "") != "_1" {
		t.Fatalf("cfg = %+v", obj)
	}
	if obj.Opt("query_tool", "odoo_search_read") != "odoo_search_read" {
		t.Error("an unset opt must fall back to its default")
	}
}

// The wiring a live run depends on: a catalog entry becomes an odooSource that loads a board.
// Every unit above tests the mapping with the source built by hand; this is the path config
// actually takes, and it is where a JSON tag or a discovery switch goes wrong silently.
func TestCatalogEntryBecomesALoadableOdooBoard(t *testing.T) {
	srv := odooServer(t, fleetBoard)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	cat := fmt.Sprintf(`{"servers":{
	  "acr-odoo": {"url":%q, "board":{"kind":"odoo","opts":{"strip_suffix":"_1"},"poll_minutes":10}},
	  "contract": {"url":"https://example.invalid/mcp", "board":true},
	  "plain":    {"command":"true"}
	}}`, srv.URL)
	if err := os.WriteFile(filepath.Join(home, ".partyline", "mcp.json"), []byte(cat), 0o600); err != nil {
		t.Fatal(err)
	}

	var odoo boardSource
	found := map[string]bool{}
	for _, s := range discoverBoardProviders() {
		found[s.Name()] = true
		if s.Name() == "acr-odoo" {
			odoo = s
		}
	}
	if !found["contract"] {
		t.Error(`"board": true must still mean the resource contract`)
	}
	if found["plain"] {
		t.Error("a server that did not opt in must not become a board")
	}
	if odoo == nil {
		t.Fatal("the odoo entry did not become a board source")
	}
	if _, ok := odoo.(odooSource); !ok {
		t.Fatalf(`kind "odoo" produced %T, not the built-in provider`, odoo)
	}
	if got := odoo.(pollingSource).PollInterval(); got != 10*time.Minute {
		t.Errorf("poll interval = %v, want the configured 10m", got)
	}

	d, err := odoo.Load("528")
	if err != nil {
		t.Fatalf("loading through the configured path: %v", err)
	}
	if d.Title(d.Keys()[0]) != "New" {
		t.Errorf("opts did not reach the provider — column reads %q", d.Title(d.Keys()[0]))
	}
	if _, ok := d.Find("28194"); !ok {
		t.Error("the board came back without its cards")
	}
}

// A kind this binary does not know must be visible as a broken board, not vanish from the picker.
func TestUnknownBoardKindShowsAsBroken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".partyline"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".partyline", "mcp.json"),
		[]byte(`{"servers":{"future":{"url":"https://x.invalid","board":{"kind":"linear"}}}}`), 0o600)

	got := discoverBoardProviders()
	if len(got) != 1 {
		t.Fatalf("want the misconfigured board listed, got %d", len(got))
	}
	_, err := got[0].Load("")
	if err == nil || !strings.Contains(err.Error(), "linear") {
		t.Fatalf("err = %v, want it to name the kind it does not know", err)
	}
}

// The overview: no project chosen shows the PROJECTS kanban, not an error telling you to press a
// key. Odoo opens on this view; standing a prompt in front of it is a worse answer than the one the
// tracker already gives.
func TestOdooOverviewIsTheProjectsBoard(t *testing.T) {
	s := fleetSource(t, nil)
	d, err := s.Load("")
	if err != nil {
		t.Fatalf("the overview must be a board, not an error: %v", err)
	}
	if len(d.Columns) != 3 {
		t.Fatalf("columns = %+v, want the project kanban stages", d.Columns)
	}
	if d.Title(d.Keys()[0]) != "New" || d.Title(d.Keys()[1]) != "Development" {
		t.Errorf("project stages out of sequence: %+v", d.Columns)
	}
	fleet, ok := d.Find("528")
	if !ok {
		t.Fatal("Fleet Manager is not on the projects board")
	}
	if fleet.Task != "Fleet Manager" || fleet.Detail != "35 tasks" {
		t.Errorf("project card = %+v", fleet)
	}
	var owner string
	for _, f := range fleet.Fields {
		if f.Label == "owner" {
			owner = f.Value
		}
	}
	if owner != "Darcy Reno" {
		t.Errorf("owner = %q, want the resolved many2one name", owner)
	}
	if _, ok := d.Find("999"); ok {
		t.Error("a project in an unlisted stage was placed anyway")
	}
}

// Drilling: a project card opens that project's tasks; a task card opens nothing.
func TestOdooProjectCardDrillsIntoItsTasks(t *testing.T) {
	s := fleetSource(t, map[string]string{"strip_suffix": "_1"})

	scope, ok := s.DrillInto("", "528")
	if !ok || scope != "528" {
		t.Fatalf("DrillInto(overview, 528) = %q %v, want the project id", scope, ok)
	}
	if _, ok := s.DrillInto("528", "28194"); ok {
		t.Error("a task must not claim to contain another board")
	}

	d, err := s.Load(scope)
	if err != nil {
		t.Fatal(err)
	}
	if d.Scope != "Fleet Manager" {
		t.Errorf("scope label = %q, want the project name", d.Scope)
	}
	if _, ok := d.Find("28194"); !ok {
		t.Error("drilling in did not produce the project's tasks")
	}
	// the task board's columns are TASK stages, not the project kanban's
	if d.Title(d.Keys()[0]) != "New" || len(d.Columns) != 2 {
		t.Errorf("task columns = %+v", d.Columns)
	}
}
