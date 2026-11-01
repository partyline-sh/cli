package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPhoneTerminalDesktopEndAndMainServerIsolation(t *testing.T) {
	tmuxTestServer(t, "ptln-test-phone-desktop")
	t.Setenv("HOME", t.TempDir())
	registry := phoneTerminalRegistry()
	defer exec.Command("tmux", "-L", registry.Socket, "kill-server").Run()
	if out, err := tmuxCmd("new-session", "-d", "-s", tmuxSessionName, "-x", "100", "-y", "30", "--", "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("main fixture: %v %s", err, out)
	}
	before, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_width}:#{window_height}").Output()
	if err != nil {
		t.Fatal(err)
	}
	s, err := registry.Create("owner", "desktop-test", "/bin/bash", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	a, err := registry.Attach("owner", s.ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, a.Master); _ = a.Wait(); close(drained) }()
	if err = a.Resize(55, 18); err != nil {
		t.Fatal(err)
	}
	after, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{window_width}:#{window_height}").Output()
	if err != nil || string(before) != string(after) {
		t.Fatal("phone resized desktop", string(before), string(after), err)
	}
	clients, err := tmuxCmd("list-clients", "-F", "#{client_pid}").Output()
	if err != nil || strings.TrimSpace(string(clients)) != "" {
		t.Fatal("phone attached to desktop server", string(clients), err)
	}
	a.Close()
	<-drained
	rows := phoneTerminalRows()
	if len(rows) != 1 || rows[0].Tool != "phone" || rows[0].resumeArgv != nil {
		t.Fatal("desktop phone entry is not list-only", rows)
	}
	if !strings.Contains(strings.Join(deleteModalLines(rows[0], deleteFacts{}), "\n"), "cannot be recovered") {
		t.Fatal("End warning missing")
	}
	if _, err = deleteSession(rows[0], nil); err != nil {
		t.Fatal(err)
	}
	if rows = phoneTerminalRows(); len(rows) != 0 {
		t.Fatal("ended terminal still listed", rows)
	}
}

func TestTerminalOutputPairingCacheExpires(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(relayDir(), "phones.json")
	raw, _ := json.Marshal([]remotePhone{{ID: "owner"}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cache := terminalPhoneCache{}
	if !cache.allowed("owner", now) {
		t.Fatal("paired owner refused")
	}
	if err := os.WriteFile(path, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if !cache.allowed("owner", now.Add(500*time.Millisecond)) {
		t.Fatal("cache reread per frame")
	}
	if cache.allowed("owner", now.Add(time.Second)) {
		t.Fatal("revocation remained cached beyond one second")
	}
}

func TestTerminalOutputPairingCacheRetainsLastSuccessfulRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(relayDir(), "phones.json")
	write := func(raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	cache := terminalPhoneCache{}
	write("invalid json")
	if cache.allowed("owner", now) {
		t.Fatal("read error allowed an unknown owner")
	}
	write(`[{"id":"owner"}]`)
	if !cache.allowed("owner", now.Add(time.Second)) {
		t.Fatal("paired owner refused")
	}
	write("invalid json")
	if !cache.allowed("owner", now.Add(2*time.Second)) {
		t.Fatal("transient read error discarded the last successful set")
	}
	if cache.allowed("unknown", now.Add(2*time.Second)) {
		t.Fatal("read error allowed an unknown owner")
	}
	write("[]")
	if cache.allowed("owner", now.Add(3*time.Second)) {
		t.Fatal("successful revocation read did not replace the cache")
	}
}
