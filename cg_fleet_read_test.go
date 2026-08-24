package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// readRepoFile reads a source file so a test can assert on wiring that has no runtime handle —
// "is this tool dispatched" is answered by the dispatch switch, and there is no way to ask it
// except by reading the switch.
func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// The planning agent could see the BACKLOG but not the machines the backlog runs on. Asked "where
// should this run" or "why is nothing moving", it had nothing to read, so it guessed or handed the
// question back — the same failure backlog_read was written for, one layer down.

func TestReadFleetIsAdvertisedToThePlanningAgent(t *testing.T) {
	var found map[string]any
	for _, d := range cgBaseToolDefs {
		if d["name"] == "read_fleet" {
			found = d
			break
		}
	}
	if found == nil {
		t.Fatal("read_fleet is not in the planning agent's tool list — the model cannot call what it cannot see")
	}
	desc, _ := found["description"].(string)
	// The two fleet-shaped tools answer different questions, and a model that confuses them either
	// sets a project up when it meant to check health, or reports "no machines" because it looked in
	// the wrong place. The description has to draw that line.
	if !strings.Contains(desc, "list_machines") {
		t.Errorf("the description must distinguish read_fleet from list_machines:\n%s", desc)
	}
	for _, want := range []string{"health", "version", "building"} {
		if !strings.Contains(strings.ToLower(desc), want) {
			t.Errorf("the description does not mention %q, so a model cannot tell what it returns:\n%s", want, desc)
		}
	}
	// It takes nothing: the fleet is a property of the caller's team, and any argument here would be
	// a way to ask about somebody else's.
	schema, _ := found["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if len(props) != 0 {
		t.Errorf("read_fleet should take no arguments, got %v", props)
	}
}

// Every advertised tool must be dispatchable. A definition with no case is a tool that exists in the
// list, gets called, and returns "unknown tool" — worse than absent, because the model tries it.
func TestReadFleetIsDispatchable(t *testing.T) {
	src := readRepoFile(t, "cg_mcp.go")
	if !strings.Contains(src, `p.Name == "read_fleet"`) {
		t.Error("read_fleet is advertised but has no dispatch branch in handleCall")
	}
	// It must not sit behind handleCall's thread guard: a machine is knowable without a thread, and
	// an agent can hold a perfectly good question about the fleet in a repo whose thread is not
	// bound yet.
	//
	// SCOPED TO handleCall on purpose. There are a dozen `s.thread == ""` guards in this file, in
	// prompt handlers and elsewhere, and comparing against the first one in the WHOLE file compares
	// against an unrelated function — which is exactly what this assertion did on its first attempt
	// and reported a failure that was not real.
	body := src[strings.Index(src, "func (s *cgServer) handleCall"):]
	dispatch := strings.Index(body, `p.Name == "read_fleet"`)
	guard := strings.Index(body, `if s.thread == "" {`)
	if dispatch < 0 {
		t.Fatal("read_fleet is not dispatched inside handleCall")
	}
	if guard > 0 && dispatch > guard {
		t.Error("read_fleet is dispatched AFTER handleCall's thread guard; the fleet does not need a thread")
	}
}

func TestFleetToolDefinitionIsWellFormed(t *testing.T) {
	for _, d := range cgBaseToolDefs {
		if d["name"] != "read_fleet" {
			continue
		}
		if _, err := json.Marshal(d); err != nil {
			t.Fatalf("read_fleet's definition does not marshal, so initialize would fail: %v", err)
		}
	}
}
