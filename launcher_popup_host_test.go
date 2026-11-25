package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestLauncherPopupHost checks the deferred menu command and sizing without contacting tmux.
func TestLauncherPopupHost(t *testing.T) {
	for _, item := range tmuxCommandItems("ptln") {
		if item.key == "o" && !reflect.DeepEqual(item.run, []string{"run-shell", "-b", "ptln tmux --home"}) {
			t.Fatalf("launcher opens synchronously: %v", item.run)
		}
	}
	// Half again the approval popup on a large client; the client less a border on a small one.
	if w, h := launcherPopupSize(300, 90); w != 144 || h != 45 {
		t.Errorf("a large client got %dx%d, want 144x45", w, h)
	}
	for _, size := range [][2]int{{120, 38}, {80, 24}, {60, 20}, {15, 7}, {3, 3}} {
		args := launcherPopupCommand("ptln", size[0], size[1])
		w, _ := strconv.Atoi(args[3])
		h, _ := strconv.Atoi(args[5])
		if w > size[0] || h > size[1] || w < 1 || h < 1 {
			t.Fatalf("%v exceeds %v", args, size)
		}
	}
}

// TestLauncherListSelectedRowVisible keeps a selection below the first page on screen.
func TestLauncherListSelectedRowVisible(t *testing.T) {
	m := launcherFixture(t)
	m.menu.toggleCollapse()
	m.menu.move(24)
	want := m.menu.displayTitle(*m.menu.selected())
	if !strings.Contains(gateANSI.ReplaceAllString(m.View().Content, ""), want) {
		t.Fatalf("selected %q is offscreen", want)
	}
}

// The cache keeps a bounded title, so its size does not grow with the length of first messages.
func TestLauncherListCacheBoundsTitles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	long := strings.Repeat("x", 40000)
	saveLauncherCache([]aiSession{{Tool: "claude", ID: "a", Title: long}}, nil)
	all, _, ok := loadLauncherSnapshot()
	if !ok || len(all) != 1 || len([]rune(all[0].Title)) != launcherCacheTitle {
		t.Fatalf("cached title: ok %v, %d sessions", ok, len(all))
	}
}

// The welcome screen's "/ find a session" reaches the popup, a separate process, as --search.
func TestLauncherListSearchTravelsToThePopup(t *testing.T) {
	old := launcherSearch
	t.Cleanup(func() { launcherSearch = old })
	launcherSearch = true
	cmd := launcherPopupCommand("ptln", 120, 40)
	if cmd[len(cmd)-1] != "--search" {
		t.Fatalf("popup command lost the search: %v", cmd)
	}
	launcherSearch = false
	if cmd := launcherPopupCommand("ptln", 120, 40); cmd[len(cmd)-1] == "--search" {
		t.Fatal("search without asking")
	}
}

// Inside someone else's tmux, bare ptln says so instead of nesting an attach.
func TestLauncherListRefusesAForeignTmux(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,123,0")
	t.Setenv("PARTYLINE_TMUX_SOCKET", "ptln-test-foreign")
	if requireTmux() != nil {
		t.Skip("usable tmux not installed")
	}
	if err := runLLMSApp(nil); err == nil || !strings.Contains(err.Error(), "inside another tmux") {
		t.Fatalf("runLLMSApp in a foreign tmux: %v", err)
	}
}
