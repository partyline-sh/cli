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
func TestReadStateIgnoresUnknownFields(t *testing.T) {
	// `peers` is what an OLDER ptln still emits; the tray no longer models it and must not choke.
	fakePtln(t, `echo '{"waiting":2,"something_new":{"a":1},"peers":{"inbound":1,"unknown":true}}'`)
	m, fail := readState()
	if fail != readOK {
		t.Fatalf("fail = %v, want readOK", fail)
	}
	if m.Waiting != 2 {
		t.Fatalf("Waiting = %d, want 2", m.Waiting)
	}
}
