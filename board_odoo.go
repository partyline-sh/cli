package main

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// board_odoo.go — an Odoo board, assembled from an existing Odoo MCP server's own tools.
//
// Nothing is asked of the server. It is a shared production server with several consumers, and a
// board is partyline's concept, not something it should have to grow a surface for. What partyline
// knows here is ODOO — project.project, project.task, project.task.type, mail.message are stock
// models, the same in every installation — and the only thing it needs from any particular server
// is one generic search_read escape hatch. That is why zero-touch works for Odoo and would not for
// a server offering nothing but curated single-purpose tools.
//
// Read-only, permanently and by design. Updating Odoo is something the agent in the session beside
// this board already does through the same server's write tools; routing it through a board
// keystroke would add a second path to production data and buy nothing.
//
// Everything installation-specific is config (see boardConfig.Opts), because it has to be:
//
//	strip_suffix   stage names here read "New_1", "Complete_1" — an artifact of a duplicated
//	               stage set in ONE database, not a fact about Odoo
//	task_url       the deep link template for this deployment ({id} is substituted)
//	query_tool     whatever this server named its generic search_read
//	scopes_tool    whatever it named its project list

type odooSource struct {
	p    providerSource
	cfg  boardConfig
	poll time.Duration
}

func (s odooSource) Name() string { return s.p.name }

// PollInterval honours "poll_minutes" from the catalog. Zero — the default — means this board is
// read when you ask for it and at no other time.
func (s odooSource) PollInterval() time.Duration { return s.poll }

// odooLimit is the ceiling the search_read tool enforces. Boards larger than this page through it
// rather than silently showing a prefix — a board missing its last forty cards looks like a working
// board, which is the worst way to be wrong.
const odooLimit = 200

// odooMaxCards bounds a whole board. A tracker with ten thousand open tasks is not a board anyone
// reads; it is a denial of service against the render path.
const odooMaxCards = 1000

// query runs one search_read and decodes the record list.
func (s odooSource) query(model string, domain []any, fields []string, order string, limit, offset int) ([]map[string]any, error) {
	args := map[string]any{"model": model, "domain": domain, "fields": fields, "limit": limit}
	if order != "" {
		args["order"] = order
	}
	if offset > 0 {
		args["offset"] = offset
	}
	body, err := s.p.callTool(s.cfg.Opt("query_tool", "odoo_search_read"), args)
	if err != nil {
		return nil, err
	}
	var out struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, fmt.Errorf("%s did not return records this board can read: %w", model, err)
	}
	return out.Records, nil
}

// ── Odoo's two encodings ─────────────────────────────────────────────────────────────────────────

// odooStr reads a string field. Odoo returns `false` — not null, not "" — for every empty value, on
// every type. Treating that as a value is how a board ends up full of the word "false".
func odooStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return "" // false = empty, and true is not a thing a text field returns
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// odooRef reads a many2one, which arrives as the pair [id, display_name] and NOT as a scalar. The
// board wants both halves: the id is a stable column key, the name is what a human reads.
func odooRef(v any) (id string, name string) {
	pair, ok := v.([]any)
	if !ok || len(pair) < 2 {
		return "", ""
	}
	switch n := pair[0].(type) {
	case float64:
		id = strconv.FormatInt(int64(n), 10)
	case string:
		id = n
	}
	return id, odooStr(pair[1])
}

func odooIDs(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range list {
		if n, ok := e.(float64); ok {
			out = append(out, strconv.FormatInt(int64(n), 10))
		}
	}
	return out
}

// ── scopes ───────────────────────────────────────────────────────────────────────────────────────

// Scopes lists the Odoo projects. The task count rides along as the note, because a board of an
// empty project is the one thing you never meant to open.
func (s odooSource) Scopes() ([]boardScope, error) {
	if tool := s.cfg.Opt("scopes_tool", "list_projects"); tool != "-" {
		if body, err := s.p.callTool(tool, map[string]any{}); err == nil {
			var out struct {
				Projects []struct {
					ID     any    `json:"id"`
					Name   string `json:"name"`
					Count  int    `json:"task_count"`
					Active *bool  `json:"active"`
				} `json:"projects"`
			}
			if json.Unmarshal([]byte(body), &out) == nil && len(out.Projects) > 0 {
				var scopes []boardScope
				for _, p := range out.Projects {
					if p.Active != nil && !*p.Active {
						continue
					}
					scopes = append(scopes, boardScope{
						ID:    safeForeignText(odooStr(p.ID)),
						Label: safeForeignText(p.Name),
						Note:  taskCountNote(p.Count),
					})
				}
				return scopes, nil
			}
		}
	}
	// A server without that convenience tool still has search_read, so the picker still works.
	recs, err := s.query("project.project", []any{[]any{"active", "=", true}},
		[]string{"id", "name"}, "name asc", odooLimit, 0)
	if err != nil {
		return nil, err
	}
	var scopes []boardScope
	for _, r := range recs {
		scopes = append(scopes, boardScope{
			ID:    safeForeignText(odooStr(r["id"])),
			Label: safeForeignText(odooStr(r["name"])),
		})
	}
	return scopes, nil
}

func taskCountNote(n int) string {
	switch {
	case n == 0:
		return "empty"
	case n == 1:
		return "1 task"
	default:
		return strconv.Itoa(n) + " tasks"
	}
}

// ── the board ────────────────────────────────────────────────────────────────────────────────────

// Load returns one of TWO boards, which is what Odoo itself shows you.
//
// With no project chosen it is the PROJECTS board: project.project laid out in its own kanban
// stages (New, Planning, Development, Support | QA …), one card per project. Choosing a project —
// ⏎ on its card, or the p picker — drills into that project's TASKS.
//
// The overview used to be an error telling you to press p. That is a worse answer than the one
// Odoo gives for free: the projects kanban IS the top of this tracker, not a prompt standing in
// front of it.
func (s odooSource) Load(scopeID string) (*boardData, error) {
	if strings.TrimSpace(scopeID) == "" {
		return s.projectsBoard()
	}
	pid, err := strconv.Atoi(strings.TrimSpace(scopeID))
	if err != nil {
		return nil, fmt.Errorf("%q is not an Odoo project id", scopeID)
	}

	cols, keys, err := s.columns(pid)
	if err != nil {
		return nil, err
	}
	cards, err := s.cards(pid, keys)
	if err != nil {
		return nil, err
	}

	d := &boardData{
		ByColumn: map[api.BoardColumn][]api.BoardCard{},
		Columns:  cols,
		Source:   s.p.name,
		Scope:    s.scopeLabel(pid),
		Live:     false, // never a ticker; g, or the configured interval, and nothing else
		ReadAt:   time.Now(),
	}
	for _, c := range cols {
		d.ByColumn[c.Key] = nil
	}
	for _, c := range cards {
		d.ByColumn[c.Column] = append(d.ByColumn[c.Column], c)
	}
	sanitizeForeignBoard(d) // every string, once, at the boundary
	return d, nil
}

// ── the projects board ───────────────────────────────────────────────────────────────────────────

// projectFields is what a project tile is built from.
var projectFields = []string{"id", "name", "stage_id", "task_count", "user_id", "partner_id", "description"}

// projectsBoard is every project in its own kanban stage — the view Odoo opens on.
func (s odooSource) projectsBoard() (*boardData, error) {
	stages, err := s.query("project.project.stage", []any{}, []string{"id", "name", "sequence"},
		"sequence asc", odooLimit, 0)
	if err != nil {
		return nil, err
	}
	if len(stages) == 0 {
		return nil, fmt.Errorf("this Odoo has no project stages")
	}

	keys := map[string]bool{}
	var cols []boardColumn
	for _, r := range stages {
		id := odooStr(r["id"])
		if id == "" || keys[id] {
			continue
		}
		keys[id] = true
		title := odooStr(r["name"])
		if title == "" {
			title = id
		}
		cols = append(cols, boardColumn{Key: api.BoardColumn(id), Title: title})
	}

	var recs []map[string]any
	for offset := 0; offset < odooMaxCards; offset += odooLimit {
		page, err := s.query("project.project", []any{[]any{"active", "=", true}},
			projectFields, "sequence asc, name asc", odooLimit, offset)
		if err != nil {
			return nil, err
		}
		recs = append(recs, page...)
		if len(page) < odooLimit {
			break
		}
	}

	d := &boardData{
		ByColumn: map[api.BoardColumn][]api.BoardCard{},
		Columns:  cols,
		Source:   s.p.name,
		Scope:    "all projects",
		Live:     false,
		ReadAt:   time.Now(),
	}
	for _, c := range cols {
		d.ByColumn[c.Key] = nil
	}

	urlTmpl := s.cfg.Opt("project_url", "")
	for _, r := range recs {
		stageID, stageName := odooRef(r["stage_id"])
		if !keys[stageID] {
			continue
		}
		id := odooStr(r["id"])
		card := api.BoardCard{
			ID:         id,
			Task:       odooStr(r["name"]),
			StateLabel: stageName,
			Column:     api.BoardColumn(stageID),
			Foreign:    true,
			Detail:     taskCountNote(atoiSafe(odooStr(r["task_count"]))),
			Body:       odooHTMLText(odooStr(r["description"])),
		}
		if urlTmpl != "" {
			card.SourceURL = strings.ReplaceAll(urlTmpl, "{id}", id)
		}
		if _, owner := odooRef(r["user_id"]); owner != "" {
			card.Fields = append(card.Fields, api.BoardCardField{Label: "owner", Value: owner})
		}
		if _, customer := odooRef(r["partner_id"]); customer != "" {
			card.Fields = append(card.Fields, api.BoardCardField{Label: "customer", Value: customer})
		}
		card.Fields = append(card.Fields, api.BoardCardField{Label: "tasks", Value: odooStr(r["task_count"])})
		d.ByColumn[card.Column] = append(d.ByColumn[card.Column], card)
	}
	sanitizeForeignBoard(d)
	return d, nil
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// DrillInto makes a project card open its own task board. On the projects board a card IS a
// container, so ⏎ descends rather than doing nothing; on a task board there is nothing below.
func (s odooSource) DrillInto(scope, cardID string) (string, bool) {
	if strings.TrimSpace(scope) != "" || strings.TrimSpace(cardID) == "" {
		return "", false
	}
	return cardID, true
}

// columns are the project's own stages, in Odoo's own order. They are NOT mapped onto partyline's
// five: a stage set is the shape the team actually works in, and flattening Waiting_On and
// Code_Review into "blocked" throws away the distinction you opened the board to see.
func (s odooSource) columns(pid int) ([]boardColumn, map[string]bool, error) {
	recs, err := s.query("project.task.type", []any{[]any{"project_ids", "in", []int{pid}}},
		[]string{"id", "name", "sequence"}, "sequence asc", odooLimit, 0)
	if err != nil {
		return nil, nil, err
	}
	if len(recs) == 0 {
		return nil, nil, fmt.Errorf("that project has no stages")
	}
	strip := s.cfg.Opt("strip_suffix", "")
	keys := map[string]bool{}
	var cols []boardColumn
	for _, r := range recs {
		id := odooStr(r["id"])
		if id == "" || keys[id] {
			continue
		}
		keys[id] = true
		title := odooStr(r["name"])
		if strip != "" {
			title = strings.TrimSuffix(title, strip)
		}
		if title == "" {
			title = id
		}
		cols = append(cols, boardColumn{Key: api.BoardColumn(id), Title: title})
	}
	return cols, keys, nil
}

// taskFields is what a tile and its detail pane are built from. Named explicitly because
// search_read without a field list pulls every column of every row.
var taskFields = []string{
	"id", "name", "stage_id", "user_ids", "partner_id",
	"date_deadline", "priority", "write_date", "description",
}

func (s odooSource) cards(pid int, keys map[string]bool) ([]api.BoardCard, error) {
	var recs []map[string]any
	for offset := 0; offset < odooMaxCards; offset += odooLimit {
		page, err := s.query("project.task", []any{[]any{"project_id", "=", pid}},
			taskFields, "priority desc, write_date desc", odooLimit, offset)
		if err != nil {
			return nil, err
		}
		recs = append(recs, page...)
		if len(page) < odooLimit {
			break // the last page is short, so there is nothing after it
		}
	}
	if len(recs) == 0 {
		return nil, nil
	}

	names := s.resolveUsers(recs)
	chatter := s.chatter(recs)
	urlTmpl := s.cfg.Opt("task_url", "")

	var out []api.BoardCard
	for _, r := range recs {
		stageID, stageName := odooRef(r["stage_id"])
		if !keys[stageID] {
			// A task in a stage the project does not list has nowhere to sit. Dropping it silently
			// is what the wire contract does; saying so is better than a card quietly missing.
			continue
		}
		id := odooStr(r["id"])
		card := api.BoardCard{
			ID:         id,
			Task:       odooStr(r["name"]),
			StateLabel: strings.TrimSuffix(stageName, s.cfg.Opt("strip_suffix", "")),
			Column:     api.BoardColumn(stageID),
			Foreign:    true,
			Detail:     odooRecency(r),
			Body:       odooBody(odooStr(r["description"]), chatter[id]),
		}
		if urlTmpl != "" {
			card.SourceURL = strings.ReplaceAll(urlTmpl, "{id}", id)
		}
		card.Fields = odooFields(r, names)
		out = append(out, card)
	}
	return out, nil
}

// odooRecency is the one line a tile has room for under the title: when it is due, or failing that
// how stale it is. A deadline outranks an edit date — it is the thing that makes a card urgent.
func odooRecency(r map[string]any) string {
	if d := odooStr(r["date_deadline"]); d != "" {
		return "due " + d
	}
	if t, ok := odooTime(odooStr(r["write_date"])); ok {
		return "updated " + odooAge(time.Since(t))
	}
	return ""
}

// odooTime parses Odoo's datetime format, which is neither RFC3339 nor timezone-qualified — it is
// UTC by convention.
func odooTime(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func odooAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "just now"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	case d < 60*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	default:
		return strconv.Itoa(int(d.Hours()/24/30)) + "mo ago"
	}
}

// odooFields is the detail pane's table. The whole point of the pane is not having to open Odoo, so
// what a person would go there for goes in — but only fields that are actually set, because a
// column of "—" teaches nothing.
func odooFields(r map[string]any, names map[string]string) []api.BoardCardField {
	var f []api.BoardCardField
	add := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			f = append(f, api.BoardCardField{Label: label, Value: value})
		}
	}
	var who []string
	for _, id := range odooIDs(r["user_ids"]) {
		if n := names[id]; n != "" {
			who = append(who, n)
		}
	}
	add("assignee", strings.Join(who, ", "))
	if _, customer := odooRef(r["partner_id"]); customer != "" {
		add("customer", customer)
	}
	add("deadline", odooStr(r["date_deadline"]))
	add("priority", odooPriority(odooStr(r["priority"])))
	add("updated", odooStr(r["write_date"]))
	return f
}

// odooPriority: Odoo stores priority as a string enum, and "0" is the default nobody ever sets —
// showing it as a priority implies a decision that was never made.
func odooPriority(v string) string {
	switch v {
	case "", "0":
		return ""
	case "1":
		return "starred"
	default:
		return v
	}
}

// resolveUsers turns the assignee ids every task carries into names, in one query rather than one
// per card. user_ids comes back as bare ids — Odoo does not expand a many2many the way it expands
// a many2one.
func (s odooSource) resolveUsers(recs []map[string]any) map[string]string {
	seen := map[string]bool{}
	var ids []int
	for _, r := range recs {
		for _, id := range odooIDs(r["user_ids"]) {
			if seen[id] {
				continue
			}
			seen[id] = true
			if n, err := strconv.Atoi(id); err == nil {
				ids = append(ids, n)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Ints(ids)
	if len(ids) > odooLimit {
		ids = ids[:odooLimit]
	}
	out := map[string]string{}
	recs, err := s.query("res.users", []any{[]any{"id", "in", ids}},
		[]string{"id", "name"}, "", odooLimit, 0)
	if err != nil {
		return nil // a board without assignee names still beats no board
	}
	for _, r := range recs {
		out[odooStr(r["id"])] = odooStr(r["name"])
	}
	return out
}

// odooChatterLimit bounds the conversation pulled for a whole board. Chatter is unbounded in
// principle — some tasks have hundreds of tracking rows — and the detail pane only ever shows one
// card's worth.
const odooChatterLimit = 200

// chatter fetches the discussion on these tasks, newest first, in ONE query for the whole board
// rather than one per card.
func (s odooSource) chatter(recs []map[string]any) map[string][]string {
	var ids []int
	for _, r := range recs {
		if n, err := strconv.Atoi(odooStr(r["id"])); err == nil {
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	msgs, err := s.query("mail.message",
		[]any{[]any{"model", "=", "project.task"}, []any{"res_id", "in", ids}},
		[]string{"res_id", "body", "author_id", "date"}, "date desc", odooChatterLimit, 0)
	if err != nil {
		return nil // the description alone is still worth showing
	}

	out := map[string][]string{}
	for i := len(msgs) - 1; i >= 0; i-- { // reverse: a conversation reads oldest first
		m := msgs[i]
		text := odooHTMLText(odooStr(m["body"]))
		if text == "" {
			continue // Odoo logs bodiless tracking rows for every field change; they are noise
		}
		_, author := odooRef(m["author_id"])
		when, _, _ := strings.Cut(odooStr(m["date"]), " ")
		line := text
		if author != "" {
			line = author + ": " + text
		}
		if when != "" {
			line = "[" + when + "] " + line
		}
		id := odooStr(m["res_id"])
		out[id] = append(out[id], line)
	}
	return out
}

func odooBody(description string, comments []string) string {
	var b strings.Builder
	if d := odooHTMLText(description); d != "" {
		b.WriteString(d)
	}
	if len(comments) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.Join(comments, "\n\n"))
	}
	return b.String()
}

var (
	odooBlockTag = regexp.MustCompile(`(?i)<(/?)(p|div|br|li|tr|h[1-6])\b[^>]*>`)
	odooAnyTag   = regexp.MustCompile(`<[^>]*>`)
	odooBlankRun = regexp.MustCompile(`\n{3,}`)
)

// odooHTMLText flattens Odoo's HTML fields to something a terminal pane can show. Odoo stores
// descriptions and chatter as HTML, so rendering them raw would fill the pane with markup.
//
// Deliberately not a parser: the output is escaped and bounded downstream by safeForeignBlock, so
// the job here is only to keep the line structure a person needs to read it.
func odooHTMLText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	s = odooBlockTag.ReplaceAllString(s, "\n")
	s = odooAnyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		lines = append(lines, strings.TrimSpace(ln))
	}
	return strings.TrimSpace(odooBlankRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// scopeLabel names the project on screen. Best-effort: the header saying the project id is a poor
// but survivable outcome, and it is not worth failing a loaded board over.
func (s odooSource) scopeLabel(pid int) string {
	recs, err := s.query("project.project", []any{[]any{"id", "=", pid}}, []string{"name"}, "", 1, 0)
	if err != nil || len(recs) == 0 {
		return strconv.Itoa(pid)
	}
	if n := odooStr(recs[0]["name"]); n != "" {
		return n
	}
	return strconv.Itoa(pid)
}
