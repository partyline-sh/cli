package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tab whose partyline session moved out keeps only a hand-started engine pane. It must be renamed
// after that engine and its directory, not keep the name of the session that left.
func TestTabRenamedAfterRemainingEnginePane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	sock := "ptln-rename-test"
	t.Setenv("PARTYLINE_TMUX_SOCKET", sock)
	dir := filepath.Join(home, "devopsforcitizendevs")
	_ = os.MkdirAll(dir, 0o755)
	fake := filepath.Join(home, "codex")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	tmux := func(args ...string) string {
		out, _ := exec.Command("tmux", append([]string{"-L", sock}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out))
	}
	defer tmux("kill-server")
	tmux("new-session", "-d", "-s", "t", "-c", dir, fake)
	tmux("new-window", "-t", "t:", "-c", home, "sleep 30") // "t:": tmux 3.8 reads a bare "t" as window 0
	for _, w := range []string{"t:0", "t:1"} {
		tmux("rename-window", "-t", w, "Partyline Original")
		tmux("set-option", "-w", "-t", w, "automatic-rename", "off")
	}
	time.Sleep(300 * time.Millisecond)

	tmuxRenameForPanes("t:0")
	if got := tmux("display-message", "-p", "-t", "t:0", "#{window_name}"); !strings.HasPrefix("codex · devopsforcitizendevs", got) || !strings.HasPrefix(got, "codex · ") {
		t.Errorf("the tab holding a hand-started codex should be named after it, got %q", got)
	}
	tmuxRenameForPanes("t:1")
	if got := tmux("show-options", "-w", "-v", "-t", "t:1", "automatic-rename"); got != "on" {
		t.Errorf("a tab with nothing nameable should get automatic naming back, got %q", got)
	}
}
