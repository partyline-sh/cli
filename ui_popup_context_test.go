package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPopupHasExplicitOpeningPane(t *testing.T) {
	tmuxTestServer(t, "ptln-test-popup-opening-pane")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TMUX_PANE", "%99999") // server environment deliberately names no real pane
	t.Setenv("PARTYLINE_UI_PANE", "")
	_ = openTagged(t, Spec{Label: "first", Key: "first", Argv: []string{"sleep", "300"}})
	covered := openTagged(t, Spec{Label: "covered", Key: "covered", Argv: []string{"sleep", "300"}})
	if err := tmuxCmd("select-window", "-t", windowOf(t, covered)).Run(); err != nil {
		t.Fatal(err)
	}
	attachPrivateTmuxClient(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	path := filepath.Join(t.TempDir(), "popup.json")
	script := `import os,json,sys;json.dump({"inherited":os.getenv("TMUX_PANE"),"ui":os.getenv("PARTYLINE_UI_PANE")},open(sys.argv[1],"w"))`
	if b, err := tmuxCmd("display-popup", "-E", "-w", "60", "-h", "12", "--", python, "-c", script, path).CombinedOutput(); err != nil {
		t.Fatalf("popup: %v %s", err, b)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["inherited"] != "%99999" || got["ui"] != covered {
		t.Fatalf("opening context: %v, covered %s", got, covered)
	}
	t.Setenv("PARTYLINE_UI_PANE", got["ui"])
	if uiPaneTarget() != covered || !motionPaneVisible() {
		t.Fatal("stale inherited pane defeated explicit popup context")
	}
}

func TestPopupClientParsingStopsAtChildCommand(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-E", "-w", "80", "-c", "/dev/ttys123", "ptln", "chat"}, "/dev/ttys123"},
		{[]string{"-E", "sh", "-c", "echo test"}, ""},
		{[]string{"-w", "80", "--", "sh", "-c", "test"}, ""},
	} {
		if got := popupClient(tc.args); got != tc.want {
			t.Fatalf("%v: %q", tc.args, got)
		}
	}
	if args := tmuxCmd("list-panes").Args; strings.Contains(strings.Join(args, " "), "PARTYLINE_UI_PANE") {
		t.Fatal("popup environment leaked into pane commands")
	}
}
