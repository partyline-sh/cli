package main

// In-pane session manager: the same switchboard the mux hosts full-screen, rendered as plain
// LINES so ptymux can place it inside a split pane (ctrl-\ |). Two independent instances exist
// while an empty split is open — one per pane — so each side has its own selection and scroll.

import (
	"strings"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// RenderLines satisfies ptymux.PaneHome: the launcher frame as position-INDEPENDENT rows
// (SGR colour only — no cursor positioning, no erase-in-line/display), sized to the pane.
func (h *llmsHome) RenderLines(cols, rows int) []string {
	if cols < 8 || rows < 3 {
		return nil
	}
	h.syncLive()
	return h.m.paneLines(cols, rows)
}

// paneLines renders the menu at pane size and converts the frame to rows. The frame builder's
// only position-dependent escapes are a leading CSI H, a per-row \x1b[K, a trailing \x1b[J and
// the modal boxes' absolute CUP — the first three are stripped (the split painter pads every
// row to pane width, which erases just as \x1b[K did) and the modals are composited here.
func (m *aiMenu) paneLines(cols, rows int) []string {
	m.w, m.h, m.narrow, m.overlays = cols, rows, true, nil
	defer func() { m.narrow, m.overlays = false, nil }()
	m.clampScroll() // a pane's height differs from the full screen's — clamp at THIS size
	lines := frameToLines(themed(m.frame()), rows)
	for _, ov := range m.overlays {
		compositeOverlay(lines, ov)
	}
	return lines
}

// frameToLines splits a launcher frame into exactly n rows, dropping the escapes that only
// make sense when the frame owns the whole terminal.
func frameToLines(frame string, n int) []string {
	frame = strings.TrimPrefix(frame, "\x1b[H")
	frame = strings.ReplaceAll(frame, "\x1b[K", "")
	frame = strings.ReplaceAll(frame, "\x1b[J", "")
	rows := strings.Split(frame, "\r\n")
	out := make([]string, n)
	for i := range out {
		if i < len(rows) {
			out[i] = rows[i]
		}
	}
	return out
}

// compositeOverlay paints a modal box into the row buffer at its (1-based) position. The row's
// tail beyond the box is dropped — the box is centered, so at pane width there is nothing but
// padding to its right, and the split painter re-pads the row to the pane's exact width.
func compositeOverlay(lines []string, ov modalOverlay) {
	for i, row := range ov.rows {
		idx := ov.top - 1 + i
		if idx < 0 || idx >= len(lines) {
			continue
		}
		lines[idx] = brand.PadTo(brand.Clip(lines[idx], ov.left-1), ov.left-1) + themed(row)
	}
}

// newPaneHome builds a FRESH manager for one split pane, sharing only the immutable/expensive
// bits with the full-screen launcher: the scanned session list, the sidecar metadata and the
// daemon presence stream (one stream per process — a pane must not open a second one). All view
// state (cursor, scroll, search, marks, modals) is this instance's own, which is the whole
// point: picking in one pane must not move the other pane's selection.
func newPaneHome(src *llmsHome) ptymux.PaneHome {
	s := src.m
	m := &aiMenu{
		all:      s.all,
		tagline:  s.tagline,
		meta:     s.meta,
		presence: s.presence,
		joinable: s.joinable,
		sort:     s.sort,
		showAll:  s.showAll,
	}
	m.setAccount(s.account())
	m.applyFilter()
	return &llmsHome{m: m, mux: src.mux, inPane: true}
}
