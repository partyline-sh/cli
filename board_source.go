package main

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"partyline.sh/partyline/internal/api"
)

// board_source.go — where a board comes from.
//
// The board used to be partyline's board and nothing else: five hard-coded columns read from
// /api/v1/board. This is the seam that lets it also show Odoo, Jira, Linear or anything anyone
// writes a provider for, through one switcher.
//
// The constraint that shapes all of it, and that predates this file (see the header of
// web/src/app/api/v1/work-items/import/route.ts): partyline holds no tracker credentials and learns
// no tracker API. A provider is a separate process, on the operator's side, that holds its own
// credentials and answers questions. Nothing about any tracker enters this codebase.

// boardColumn is one column of whatever board is loaded.
//
// The COLUMNS COME FROM THE SOURCE, which is the whole point. partyline's five are opinionated —
// chains, readiness gates, a review gate — and a nine-status Jira board squeezed into them loses
// exactly the thing you opened it for. When you are looking at Odoo you are looking at Odoo.
type boardColumn struct {
	Key   api.BoardColumn
	Title string
}

// boardData is a loaded board: its columns, its cards, and how it should be treated.
type boardData struct {
	Columns  []boardColumn
	ByColumn map[api.BoardColumn][]api.BoardCard

	Source string // display name of the source ("partyline", "odoo")
	Scope  string // display label of the active scope, if the source has scopes

	// Live sources auto-refresh; foreign ones do not. A foreign board is read when you are deciding
	// what to pick up, not watched — and polling somebody's Odoo every five seconds is rude. The
	// board STOPS its timer for a non-live source rather than running it and discarding the result.
	Live   bool
	ReadAt time.Time // when the source actually read its data — shown so a manual board admits its age
}

// Column returns one column's cards.
func (b *boardData) Column(c api.BoardColumn) []api.BoardCard {
	if b == nil {
		return nil
	}
	return b.ByColumn[c]
}

// Keys is the column order.
func (b *boardData) Keys() []api.BoardColumn {
	if b == nil {
		return nil
	}
	out := make([]api.BoardColumn, 0, len(b.Columns))
	for _, c := range b.Columns {
		out = append(out, c.Key)
	}
	return out
}

// Title is a column's heading, falling back to its key for a column the source did not describe.
func (b *boardData) Title(c api.BoardColumn) string {
	if b != nil {
		for _, col := range b.Columns {
			if col.Key == c {
				return col.Title
			}
		}
	}
	return c.Title()
}

// Find locates a card by id anywhere on the loaded board.
func (b *boardData) Find(id string) (api.BoardCard, bool) {
	if b == nil {
		return api.BoardCard{}, false
	}
	for _, k := range b.Keys() {
		for _, c := range b.ByColumn[k] {
			if c.ID == id {
				return c, true
			}
		}
	}
	return api.BoardCard{}, false
}

// boardScope is one selectable slice of a source: an Odoo project, a Jira board, a Linear team.
//
// Universal rather than an Odoo special case. Adding "project" for Odoo now means adding "board" for
// Jira later and "team" for Linear after that; one concept covers all three. Flat by design —
// trackers nest, and a provider flattens to a label like "ACR / POS" rather than us shipping a tree
// nobody has needed yet.
type boardScope struct {
	ID    string
	Label string
	Note  string
}

// boardSource is a board the switcher can show.
type boardSource interface {
	// Name is the switcher label, and the key the remembered scope is stored under.
	Name() string
	// Scopes lists what can be selected. Empty means the source has exactly one board and no picker
	// is offered.
	Scopes() ([]boardScope, error)
	// Load reads the board for a scope ("" when the source has none).
	Load(scopeID string) (*boardData, error)
}

// ── the built-in source ──────────────────────────────────────────────────────────────────────────

// partylineSource is partyline's own board: the five columns, live-refreshing, with every action.
type partylineSource struct{ c *api.Client }

func (partylineSource) Name() string { return "partyline" }

// Scopes is empty: partyline shows one board for the org.
//
// It is the obvious place to add project scoping later, and doing so would make the built-in path
// identical to a foreign one — which is the test of whether this abstraction is right. Not required
// for the first provider, so not built on spec.
func (partylineSource) Scopes() ([]boardScope, error) { return nil, nil }

func (s partylineSource) Load(string) (*boardData, error) {
	b, err := s.c.ReadBoard()
	if err != nil {
		return nil, err
	}
	return boardFromAPI(b), nil
}

// boardFromAPI adapts partyline's own board onto the generic shape.
func boardFromAPI(b *api.Board) *boardData {
	d := &boardData{
		ByColumn: map[api.BoardColumn][]api.BoardCard{},
		Source:   "partyline",
		Live:     true,
		ReadAt:   time.Now(),
	}
	for _, k := range api.BoardColumns {
		d.Columns = append(d.Columns, boardColumn{Key: k, Title: k.Title()})
		d.ByColumn[k] = b.Column(k)
	}
	return d
}

// ── sanitizing ───────────────────────────────────────────────────────────────────────────────────

// maxForeignField bounds any single string a provider hands us. A tracker description pasted whole
// into a tile would blow out the render path; the tile clips anyway, so nothing is lost by capping
// well above what any tile can show.
const maxForeignField = 2000

// safeForeignText makes one string from a provider safe to render in a terminal.
//
// Card text is written by whoever files tickets in someone else's tracker, and it lands in a
// renderer that draws with escape sequences. brand.VisWidth MEASURES escapes as zero-width, so an
// ESC embedded in a ticket title passes the width arithmetic and reaches the screen intact, where it
// can repaint, reposition the cursor, or worse. Harmless for partyline's own run output; not
// harmless for a title somebody outside your team wrote.
//
// Sanitizing happens ONCE, at the boundary where foreign data enters — not at each render site, so
// no future render path can forget to do it.
func safeForeignText(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r':
			b.WriteRune(' ') // a tile is one line per field; a newline would break the layout
		case r == unicode.ReplacementChar:
			continue
		case unicode.IsControl(r):
			continue // C0 and C1, ESC included
		case !unicode.IsPrint(r) && !unicode.IsSpace(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxForeignField {
		out = out[:maxForeignField]
	}
	return out
}

// maxForeignBody bounds a card's prose. A tracker description can be enormous; the pane scrolls, but
// carrying a megabyte of HTML-turned-text per card across a whole board is a different problem.
const maxForeignBody = 20000

// safeForeignBlock is safeForeignText for text that is MEANT to have lines.
//
// The tile sanitizer flattens newlines, which is right for a field that must occupy one line and
// wrong for a task description — running the whole thing into one paragraph is how "everything you
// would go to Odoo for" becomes unreadable. So line structure survives here and control characters
// still do not.
func safeForeignBlock(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\t", "    ")

	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == unicode.ReplacementChar:
			continue
		case unicode.IsControl(r):
			continue // C0 and C1, ESC included
		case !unicode.IsPrint(r) && !unicode.IsSpace(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxForeignBody {
		out = out[:maxForeignBody] + "\n\n… (truncated)"
	}
	return out
}

// safeForeignURL keeps only what is safe to hand to a browser. Same rule the board already applies
// in openURL, enforced here too so a provider cannot smuggle a javascript: or file: URL into a card
// and have it opened by a keystroke.
func safeForeignURL(s string) string {
	s = safeForeignText(s)
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	return ""
}

// sanitizeForeignBoard cleans every string on a provider's board, in place.
func sanitizeForeignBoard(d *boardData) {
	if d == nil {
		return
	}
	d.Source = safeForeignText(d.Source)
	d.Scope = safeForeignText(d.Scope)
	for i := range d.Columns {
		d.Columns[i].Title = safeForeignText(d.Columns[i].Title)
	}
	for key, cards := range d.ByColumn {
		for i := range cards {
			c := &cards[i]
			c.ID = safeForeignText(c.ID)
			c.Task = safeForeignText(c.Task)
			c.Title = safeForeignText(c.Title)
			c.Detail = safeForeignText(c.Detail)
			c.StateLabel = safeForeignText(c.StateLabel)
			c.Machine = safeForeignText(c.Machine)
			c.SourceURL = safeForeignURL(c.SourceURL)
			c.PRURL = safeForeignURL(c.PRURL)
			c.Body = safeForeignBlock(c.Body)
			for j := range c.Fields {
				c.Fields[j].Label = safeForeignText(c.Fields[j].Label)
				c.Fields[j].Value = safeForeignText(c.Fields[j].Value)
			}
			c.Column = key
			c.Foreign = true
		}
		d.ByColumn[key] = cards
	}
}

// sortedScopes orders a scope list for the picker: by label, so a long Odoo project list is
// navigable rather than in whatever order the tracker happened to return.
func sortedScopes(in []boardScope) []boardScope {
	out := append([]boardScope(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label)
	})
	return out
}
