package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemotePWA(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, []byte("shipped app"), 0600); err != nil {
		t.Fatal(err)
	}
	digests, err := relayAssetDigests(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer server.Close()
	c := relayConfig{URL: server.URL, Assets: digests}
	if err = relayCheckAssets(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("changed app"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = relayCheckAssets(context.Background(), c); err == nil || !strings.Contains(err.Error(), "mismatch: /index.html") {
		t.Fatalf("changed asset: %v", err)
	}
	c.Assets = nil
	if err = relayCheckAssets(context.Background(), c); err == nil {
		t.Fatal("missing baseline accepted")
	}
}

// Exercise real tmux session scoping as well as the browser's separate-pane fixture.
func TestRemotePWAContractorPane(t *testing.T) {
	tmuxTestServer(t, "ptln-test-remote-pwa")
	open := func(session string) string {
		t.Helper()
		out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", session, "sleep", "60").CombinedOutput()
		if err != nil {
			t.Fatalf("create %s: %v: %s", session, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// A contractor must remain viewable even without a person's ptln session.
	pane := open(contractorSession)
	unrelated := open(tmuxSessionName + "-unrelated")
	check := func() {
		t.Helper()
		data, err := remoteSnapshot("sessions", "focus")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range data.([]map[string]string) {
			if row["pane"] == unrelated {
				t.Fatal("unrelated session exposed")
			}
			if row["pane"] == pane {
				found = true
				if row["window"] != windowOf(t, pane) || row["width"] == "" || row["height"] == "" || row["session"] != contractorSession {
					t.Fatalf("incomplete contractor inventory: %v", row)
				}
			}
		}
		if !found {
			t.Fatal("contractor missing from authorized inventory")
		}
		if _, err := remoteSnapshot(pane, "focus"); err != nil {
			t.Fatalf("contractor capture: %v", err)
		}
		if _, err := remoteSnapshot(unrelated, "focus"); err == nil {
			t.Fatal("unrelated pane capture authorized")
		}
	}
	check()
	open(tmuxSessionName)
	check()
}
