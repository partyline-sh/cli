package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// board_switch.go — choosing WHICH board is on screen: the source, and the scope inside it.

// activeSource is the source currently showing.
func (m *boardModel) activeSource() boardSource {
	if len(m.sources) == 0 {
		return nil
	}
	if m.src < 0 || m.src >= len(m.sources) {
		m.src = 0
	}
	return m.sources[m.src]
}

// sourceLabel is what the header says you are looking at: the source, and the scope inside it when
// there is one. "partyline" alone, or "odoo · ACR POS".
func (m *boardModel) sourceLabel() string {
	s := m.activeSource()
	if s == nil {
		return "work board"
	}
	label := s.Name()
	if m.data != nil && m.data.Scope != "" {
		label += " · " + m.data.Scope
	}
	return label
}

// loadActive reads the current source at the current scope.
func (m *boardModel) loadActive() (*boardData, error) {
	s := m.activeSource()
	if s == nil {
		return nil, fmt.Errorf("no board source")
	}
	d, err := s.Load(m.scope)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("%s returned no board", s.Name())
	}
	return d, nil
}

// pickSource opens the board picker: every connected board, the current one preselected.
//
// This used to cycle with each press, on the reasoning that with two or three boards a keystroke
// beats a modal. That reasoning expires at the scale this is now for — partyline plus Odoo plus
// Jira plus Linear means pressing past boards you did not want, each one firing a read on arrival.
// A list also does what a cycle cannot: name a board that is misconfigured or unreachable, instead
// of letting you select it and discover the problem as a toast.
func (m *boardModel) pickSource(c *api.Client) bool {
	if len(m.sources) < 2 {
		m.setToast("only partyline is connected — add a board with + in this list", false)
	}
	items := make([]pickerItem, 0, len(m.sources)+1)
	for i, s := range m.sources {
		note := loadBoardScopeLabel(s.Name())
		if why := m.srcErr[s.Name()]; why != "" {
			note = "— " + firstLineOf(why) // the reason, where the scope would have been
		}
		label := s.Name()
		if i == m.src {
			label += "  ◀"
		}
		items = append(items, pickerItem{Label: label, Note: note, Value: strconv.Itoa(i)})
	}
	// Adding a board and choosing between the ones you have are the same question half a second
	// apart, so they are one surface rather than two keys to remember.
	items = append(items, pickerItem{Label: "+ add a board…", Value: addBoardChoice})

	m.openOverlay(&pickerOverlay{
		heading: "boards",
		items:   items,
		sel:     m.src,
		onPick: func(m *boardModel, c *api.Client, v pickerItem) bool {
			if v.Value == addBoardChoice {
				return m.beginAddBoard(c)
			}
			i, err := strconv.Atoi(v.Value)
			if err != nil || i < 0 || i >= len(m.sources) || i == m.src {
				return false
			}
			m.src = i
			m.scope = loadBoardScope(m.activeSource().Name())
			m.data, m.focusID, m.focusChainID = nil, "", ""
			m.col = 0
			m.setPendingToast("reading " + m.activeSource().Name() + "…")
			return true
		},
	})
	return false
}

// addBoardChoice is the sentinel for the picker's last row. A value no source index can produce.
const addBoardChoice = "\x00add"

// drillSource is the optional half of boardSource for trackers that NEST: an Odoo project is both
// a card on one board and the container of another. Optional so writing a flat provider stays a
// two-method job.
type drillSource interface {
	// DrillInto reports the scope a card opens into, or ok=false when nothing is below it.
	DrillInto(scope, cardID string) (string, bool)
}

// drillInto descends from a container card to the board it holds — a project card to that
// project's tasks. Bound to ⏎, which on a foreign board had no meaning before.
func (m *boardModel) drillInto(c *api.Client) bool {
	s, ok := m.activeSource().(drillSource)
	if !ok {
		return false
	}
	card, focused := m.focused()
	if !focused {
		return false
	}
	scope, ok := s.DrillInto(m.scope, card.ID)
	if !ok {
		return false
	}
	m.scope = scope
	saveBoardScope(s.(boardSource).Name(), scope, card.Task)
	m.data, m.focusID, m.focusChainID = nil, "", ""
	m.col = 0
	m.setPendingToast("opening " + card.Task + "…")
	return true
}

// canDrill reports whether ⏎ would descend from here, so the hint bar only offers it where it works.
func (m *boardModel) canDrill() bool {
	s, ok := m.activeSource().(drillSource)
	if !ok {
		return false
	}
	c, focused := m.focused()
	if !focused {
		return false
	}
	_, ok = s.DrillInto(m.scope, c.ID)
	return ok
}

// scoped reports whether the active source has scopes to choose between, which is what decides
// whether the p key means anything here.
func (m *boardModel) scoped() bool {
	_, flat := m.activeSource().(partylineSource)
	return m.activeSource() != nil && !flat
}

// pickScope asks for the current source's scopes; the picker opens when they arrive.
//
// The listing runs off the event loop. Inline, it froze the board for the length of a network call
// to somebody else's tracker — no repaint, no indicator, nothing to distinguish slow from hung.
func (m *boardModel) pickScope(c *api.Client) bool {
	s := m.activeSource()
	if s == nil {
		return false
	}
	m.fetchScopes(s)
	return false
}

// applyScopes opens the picker on a finished listing, unless the board moved on while it ran.
func (m *boardModel) applyScopes(source string, scopes []boardScope, err error) {
	m.endBusy()
	s := m.activeSource()
	if s == nil || s.Name() != source {
		return // switched boards mid-flight; this answer is for a board nobody is looking at
	}
	if err != nil {
		m.setToast(s.Name()+" could not list its projects: "+err.Error(), true)
		return
	}
	if len(scopes) == 0 {
		m.setToast(s.Name()+" has one board — nothing to choose", false)
		return
	}

	items := make([]pickerItem, 0, len(scopes)+1)
	// Descending into a project must not be one-way. This row is the way back to the overview, and
	// it lives in the picker rather than on a key of its own because "which project" and "no
	// project, show me all of them" are the same question.
	if _, nests := s.(drillSource); nests && m.scope != "" {
		items = append(items, pickerItem{Label: "← all projects", Note: "the overview", Value: ""})
	}
	for _, sc := range sortedScopes(scopes) {
		items = append(items, pickerItem{Label: sc.Label, Note: sc.Note, Value: sc.ID})
	}
	m.openOverlay(&pickerOverlay{
		heading: s.Name() + " — which project?",
		items:   items,
		onPick: func(m *boardModel, c *api.Client, v pickerItem) bool {
			m.scope = v.Value
			saveBoardScope(s.Name(), v.Value, v.Label)
			m.data, m.focusID, m.focusChainID = nil, "", ""
			m.col = 0
			m.beginBusy("reading " + s.Name())
			return true
		},
	})
}

// ── remembered scope ─────────────────────────────────────────────────────────────────────────────

// boardScopePath is where the board remembers, per source, which scope you were last looking at.
//
// A picker you have to answer on every launch is one people stop using — the same reasoning as
// ~/.partyline/instance. Local view state, so it sits with the other machine-local preferences
// rather than in the per-endpoint config dir.
func boardScopePath() string { return filepath.Join(stateDir(), "board-scopes.json") }

// boardScopeMemo is the remembered scope AND the name it had, so the board picker can say which
// project each board is pointed at without a network call to look the name up again.
type boardScopeMemo struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// UnmarshalJSON accepts the bare id string earlier versions wrote, so an existing file keeps
// working and simply has no labels until the next time a scope is picked.
func (b *boardScopeMemo) UnmarshalJSON(data []byte) error {
	var id string
	if json.Unmarshal(data, &id) == nil {
		b.ID = id
		return nil
	}
	type raw boardScopeMemo
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*b = boardScopeMemo(r)
	return nil
}

func loadBoardScopes() map[string]boardScopeMemo {
	b, err := os.ReadFile(boardScopePath())
	if err != nil {
		return map[string]boardScopeMemo{}
	}
	out := map[string]boardScopeMemo{}
	if json.Unmarshal(b, &out) != nil {
		return map[string]boardScopeMemo{}
	}
	return out
}

func loadBoardScope(source string) string { return loadBoardScopes()[source].ID }

func loadBoardScopeLabel(source string) string {
	m := loadBoardScopes()[source]
	if m.Label != "" {
		return m.Label
	}
	return m.ID
}

func saveBoardScope(source, scope, label string) {
	all := loadBoardScopes()
	if strings.TrimSpace(scope) == "" {
		delete(all, source)
	} else {
		all[source] = boardScopeMemo{ID: scope, Label: label}
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(boardScopePath()), 0o755)
	_ = os.WriteFile(boardScopePath(), b, 0o600)
}

// ── freshness ────────────────────────────────────────────────────────────────────────────────────

// freshnessNote is what the status line says about how current the board is.
//
// A live source says nothing — it refreshes itself and claiming "just now" every five seconds is
// noise. A manual one MUST say its age: a board that looks live but is not is worse than one that
// admits it, because the whole point of looking is to decide what to do next.
func freshnessNote(d *boardData) string {
	if d == nil || d.Live {
		return ""
	}
	age := time.Since(d.ReadAt)
	switch {
	case d.ReadAt.IsZero():
		return "g to refresh"
	case age < time.Minute:
		return "read just now · g to refresh"
	case age < time.Hour:
		return fmt.Sprintf("read %dm ago · g to refresh", int(age.Minutes()))
	}
	return fmt.Sprintf("read %dh ago · g to refresh", int(age.Hours()))
}
