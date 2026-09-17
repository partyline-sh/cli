//go:build darwin && tray

package main

// state_test.go — the tray's one piece of non-GUI logic: turning a `ptln state` invocation into a
// snapshot or a classified failure.
//
// THIS FILE NEEDS THE BUILD TAG, so it does NOT run under `go test ./...`. Run it with:
//
//	CGO_ENABLED=1 go test -tags tray ./cmd/ptln-tray/
//
// Everything that could be tested WITHOUT the tag was moved to internal/traypeer, which is covered by
// the ordinary `go test ./...` walk. What's left here genuinely needs a `ptln` on PATH, and what's left
// in main.go besides this needs a menu bar.

import (
	"os"
	"path/filepath"
	"testing"
)

// fakePtln writes an executable `ptln` into a fresh dir and points PATH at it alone.
func fakePtln(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ptln"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestReadStateNoCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing on PATH at all
	if _, fail := readState(); fail != readNoCLI {
		t.Fatalf("readState() fail = %v, want readNoCLI", fail)
	}
}

// A `ptln` that's present but can't print state is readTooOld, NOT readNoCLI — the advice differs, and
// telling someone to install a binary that's sitting right there sends them hunting for nothing.
func TestReadStateTooOld(t *testing.T) {
	fakePtln(t, "exit 1")
	if _, fail := readState(); fail != readTooOld {
		t.Fatalf("failing ptln: fail = %v, want readTooOld", fail)
	}

	fakePtln(t, "echo 'usage: ptln <command>'") // exits 0, prints something that isn't JSON
	if _, fail := readState(); fail != readTooOld {
		t.Fatalf("non-JSON ptln: fail = %v, want readTooOld", fail)
	}
}

// A `ptln` old enough to print state but predating peer messaging omits the key entirely. That must
// read OK with a nil peer snapshot — the tray degrades to a hidden section, silently.
func TestReadStateOlderCLIHasNoPeers(t *testing.T) {
	fakePtln(t, `echo '{"version":"0.8.0","waiting":2,"sessions":[]}'`)
	m, fail := readState()
	if fail != readOK {
		t.Fatalf("fail = %v, want readOK", fail)
	}
	if m.Peers != nil {
		t.Fatalf("Peers = %+v, want nil against a peer-less CLI", m.Peers)
	}
	if got := m.Waiting + m.Peers.Blocked(); got != 2 {
		t.Fatalf("badge = %d, want 2 (nil peers must contribute nothing)", got)
	}
}

func TestReadStateWithPeers(t *testing.T) {
	fakePtln(t, `echo '{"waiting":1,"peers":{"inbound":2,"answered":1,"auto_answered":3,"auto_project":"partyline","consults":[{"id":"c1","project":"partyline","question":"is this index used?","waiting_sec":30}]}}'`)
	m, fail := readState()
	if fail != readOK {
		t.Fatalf("fail = %v, want readOK", fail)
	}
	if m.Peers == nil {
		t.Fatal("Peers = nil, want the decoded snapshot")
	}
	if m.Peers.Inbound != 2 || m.Peers.AutoAnswered != 3 || m.Peers.AutoProject != "partyline" {
		t.Fatalf("peers decoded wrong: %+v", *m.Peers)
	}
	if len(m.Peers.Consults) != 1 || m.Peers.Consults[0].ID != "c1" || m.Peers.Consults[0].WaitingSec != 30 {
		t.Fatalf("consults decoded wrong: %+v", m.Peers.Consults)
	}
	// waiting + inbound: a teammate's question is a person blocked on you, so it joins the badge.
	if got := m.Waiting + m.Peers.Blocked(); got != 3 {
		t.Fatalf("badge = %d, want 3", got)
	}
}

// An unknown field from a NEWER ptln must not break an older tray.
func TestReadStateIgnoresUnknownFields(t *testing.T) {
	fakePtln(t, `echo '{"waiting":0,"something_new":{"a":1},"peers":{"inbound":1,"unknown":true}}'`)
	m, fail := readState()
	if fail != readOK {
		t.Fatalf("fail = %v, want readOK", fail)
	}
	if m.Peers.Blocked() != 1 {
		t.Fatalf("Blocked() = %d, want 1", m.Peers.Blocked())
	}
}
