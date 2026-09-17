package main

import (
	"testing"

	"partyline.sh/partyline/internal/api"
)

// `m` on a partyline card must offer exactly the actions that CHANGE ITS COLUMN — nothing the `a`
// menu could not do, and nothing that merely removes the card.
func TestPartylineMovesAreColumnChangesOnly(t *testing.T) {
	// A failed card offers retry/restart (→ Building), To backlog (→ Backlog) and Discard.
	// Discard is not a move; the rest are.
	card := api.BoardCard{ID: "r1", Status: "failed", Column: api.ColBlocked}
	moves := partylineMoves(card)
	if len(moves) == 0 {
		t.Fatal("a failed card has legal moves and the picker offered none")
	}
	for _, mv := range moves {
		if mv.Value == "discard" || mv.Value == "delete" {
			t.Errorf("%q removes the card — it is not a move", mv.Value)
		}
		if dest, ok := moveDestination(mv.Value); !ok || dest == card.Column {
			t.Errorf("%q offered as a move but lands nowhere new (dest %q)", mv.Value, dest)
		}
	}
}

func TestPartylineMovesSkipTheColumnTheCardIsIn(t *testing.T) {
	// A queued Backlog card can Start (→ Building); To-backlog would be a no-op and must not show.
	card := api.BoardCard{ID: "r2", Status: "queued", Column: api.ColBacklog}
	for _, mv := range partylineMoves(card) {
		if dest, _ := moveDestination(mv.Value); dest == api.ColBacklog {
			t.Errorf("offered a move into the column the card already occupies: %q", mv.Value)
		}
	}
}

// The Odoo payload builders are the wire contract with the tracker's curated tools — pin them.
func TestOdooUpdateArgs(t *testing.T) {
	args, err := odooUpdateArgs("341", "17", "")
	if err != nil {
		t.Fatal(err)
	}
	if args["id"] != 341 || args["stage_id"] != 17 {
		t.Fatalf("stage move payload wrong: %+v", args)
	}
	if _, has := args["name"]; has {
		t.Fatal("a stage move must not carry a name — omitted fields stay untouched")
	}

	args, err = odooUpdateArgs("341", "", "New title")
	if err != nil {
		t.Fatal(err)
	}
	if args["name"] != "New title" {
		t.Fatalf("title payload wrong: %+v", args)
	}
	if _, has := args["stage_id"]; has {
		t.Fatal("a retitle must not carry a stage")
	}

	if _, err := odooUpdateArgs("not-a-number", "17", ""); err == nil {
		t.Fatal("a non-numeric task id must refuse, not reach the server")
	}
	if _, err := odooUpdateArgs("341", "junk", ""); err == nil {
		t.Fatal("a non-numeric stage id must refuse")
	}
}

func TestOdooNoteArgs(t *testing.T) {
	args, err := odooNoteArgs("77", "checked the till logs — spooler was wedged")
	if err != nil {
		t.Fatal(err)
	}
	if args["model"] != "project.task" || args["id"] != 77 || args["body"] == "" {
		t.Fatalf("note payload wrong: %+v", args)
	}
}

func TestRequireTaskScope(t *testing.T) {
	if err := requireTaskScope(""); err == nil {
		t.Fatal("the projects overview must refuse writes with directions, not attempt them")
	}
	if err := requireTaskScope("528"); err != nil {
		t.Fatalf("a task board must be writable: %v", err)
	}
}
