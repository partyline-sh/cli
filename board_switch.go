package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// nextSource cycles to the next installed source, restoring that source's remembered scope.
//
// Cycling rather than a picker: with two or three sources a key press is faster than a modal, and
// the header always says where you are. The scope picker is the separate key, because source and
// scope are different questions and one flat list of every scope across every tracker gets long
// immediately.
func (m *boardModel) nextSource(c *api.Client) bool {
	if len(m.sources) < 2 {
		m.setToast("only partyline is installed — add a board provider to switch", false)
		return false
	}
	m.src = (m.src + 1) % len(m.sources)
	m.scope = loadBoardScope(m.activeSource().Name())
	m.data, m.focusID, m.focusChainID = nil, "", ""
	m.col = 0
	m.setPendingToast("reading " + m.activeSource().Name() + "…")
	return true
}

// pickScope opens the scope picker for the current source.
func (m *boardModel) pickScope(c *api.Client) bool {
	s := m.activeSource()
	if s == nil {
		return false
	}
	scopes, err := s.Scopes()
	if err != nil {
		m.setToast(s.Name()+" could not list its projects: "+err.Error(), true)
		return false
	}
	if len(scopes) == 0 {
		m.setToast(s.Name()+" has one board — nothing to choose", false)
		return false
	}

	items := make([]pickerItem, 0, len(scopes))
	for _, sc := range sortedScopes(scopes) {
		items = append(items, pickerItem{Label: sc.Label, Note: sc.Note, Value: sc.ID})
	}
	m.openOverlay(&pickerOverlay{
		heading: s.Name() + " — which project?",
		items:   items,
		onPick: func(m *boardModel, c *api.Client, v pickerItem) bool {
			m.scope = v.Value
			saveBoardScope(s.Name(), v.Value)
			m.data, m.focusID, m.focusChainID = nil, "", ""
			m.col = 0
			return true
		},
	})
	return false
}

// ── remembered scope ─────────────────────────────────────────────────────────────────────────────

// boardScopePath is where the board remembers, per source, which scope you were last looking at.
//
// A picker you have to answer on every launch is one people stop using — the same reasoning as
// ~/.partyline/instance. Local view state, so it sits with the other machine-local preferences
// rather than in the per-endpoint config dir.
func boardScopePath() string { return filepath.Join(stateDir(), "board-scopes.json") }

func loadBoardScopes() map[string]string {
	b, err := os.ReadFile(boardScopePath())
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	if json.Unmarshal(b, &out) != nil {
		return map[string]string{}
	}
	return out
}

func loadBoardScope(source string) string { return loadBoardScopes()[source] }

func saveBoardScope(source, scope string) {
	all := loadBoardScopes()
	if strings.TrimSpace(scope) == "" {
		delete(all, source)
	} else {
		all[source] = scope
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
