package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Deleting a loop stops it and removes it for good; a batch step takes its whole batch.
func TestLoopDelete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := testLoopRun(t, t.TempDir())
	if what, _ := loopDeleteTarget(r.ID); what != "loop "+r.ID {
		t.Errorf("target %q", what)
	}
	if err := loopDelete(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(loopDir(r.ID)); !os.IsNotExist(err) {
		t.Error("the deleted loop's records are still there")
	}
	if _, err := loadLoopRun(r.ID); err == nil {
		t.Error("a deleted loop still loads")
	}

	_, b, step := approvedBatch(t)
	if what, _ := loopDeleteTarget(step.ID); !strings.HasPrefix(what, "batch "+b.ID) {
		t.Errorf("a step's target %q", what)
	}
	if err := loopDelete(step.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBatch(b.ID); err == nil {
		t.Error("the batch survived deleting its step")
	}
	for _, id := range b.Runs {
		if _, err := os.Stat(loopDir(id)); !os.IsNotExist(err) {
			t.Errorf("batch run %s survived", id)
		}
	}
}

// d arms deleting the selected card and says what goes; a second d deletes it; any other key
// keeps it.
func TestLoopsBoardDelete(t *testing.T) {
	rows := []tabRow{
		{Section: tabInProgress, RunID: "a1", Title: "one"},
		{Section: tabPlanned, Title: "two", Batch: "step 2 of 2", Detail: "batch bb1"},
	}
	var removed []string
	m := newTabModel(tabActions{rows: func(time.Time) []tabRow { return rows }, now: time.Now,
		detail: func(tabRow) string { return "" }, gateRun: func(tabRow) error { return nil },
		remove: func(id string) error { removed = append(removed, id); return nil },
		target: func(id string) (string, error) { return "loop " + id, nil }})
	press := func(k string) {
		nm, _ := m.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
		m = nm.(tabModel)
	}
	m.col = tabInProgress
	m.selectCard()
	press("d")
	if len(removed) != 0 || !strings.Contains(m.deleteNote, "d again deletes loop a1") {
		t.Fatalf("first d: removed %v, note %q", removed, m.deleteNote)
	}
	press("j") // another key keeps it
	press("d")
	press("d")
	if len(removed) != 1 || removed[0] != "a1" {
		t.Fatalf("removed %v", removed)
	}
	// A planned step deletes its batch.
	m.col = tabPlanned
	m.selectCard()
	press("d")
	press("d")
	if len(removed) != 2 || removed[1] != "bb1" {
		t.Errorf("planned step removed %v", removed)
	}
}
