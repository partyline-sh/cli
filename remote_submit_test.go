package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/remote"
)

// A real terminal composer with Codex's 120ms paste-burst Enter guard. Unlike
// a byte recorder, this distinguishes text reaching a prompt from submitting it.
// Guard: github.com/openai/codex, codex-rs/tui/src/bottom_pane/paste_burst.rs.
func TestRemoteTextSubmitsAfterPasteBurst(t *testing.T) {
	tmuxTestServer(t, "ptln-test-remote-submit")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	path := filepath.Join(t.TempDir(), "submissions")
	script := `import os,sys,tty,time,json
 tty.setraw(0)
 os.write(1,b"\x1b[?2004h")
 open(sys.argv[1]+".ready","w").close()
 pending=b""; text=b""; paste=False; last=0
 while True:
  pending+=os.read(0,65536)
  while pending:
   if pending.startswith(b"\x1b[200~"):
    paste=True; pending=pending[6:]; continue
   if pending.startswith(b"\x1b[201~"):
    paste=False; last=time.monotonic(); pending=pending[6:]; continue
   if pending[0]==27 and len(pending)<6: break
   char=pending[:1]; pending=pending[1:]; now=time.monotonic()
   if char==b"\r" and not paste and now-last>=0.12:
    with open(sys.argv[1],"a",buffering=1) as f: f.write(json.dumps(text.decode("utf-8"))+"\n")
    text=b""
   else:
    text+=b"\n" if char==b"\r" else char
    last=now
 `
	script = strings.ReplaceAll(script, "\n ", "\n")
	out, err := tmuxCmd("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "--", python, "-u", "-c", script, path).CombinedOutput()
	if err != nil {
		t.Fatalf("composer: %v %s", err, out)
	}
	pane := strings.TrimSpace(string(out))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("composer not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	store, route := remoteInputFixture(t)
	read := func() []string {
		b, _ := os.ReadFile(path)
		var messages []string
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if line == "" {
				continue
			}
			var text string
			if err := json.Unmarshal([]byte(line), &text); err != nil {
				t.Fatal(err)
			}
			messages = append(messages, text)
		}
		return messages
	}
	for i, text := range []string{"Hello from mobile", "First line\nSecond line", "Unicode café — ready;"} {
		m := remote.Message{Type: "input", ID: fmt.Sprintf("submit-%d", i), Pane: pane, Kind: "text", Data: text}
		if result := store.handle(route, m, time.Now()); result.Status != "ok" {
			t.Fatal(result)
		}
		deadline = time.Now().Add(time.Second)
		for len(read()) < i+1 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		got := read()
		if len(got) != i+1 || got[i] != text {
			t.Fatalf("message stayed in the composer instead of submitting: got %q, want %q", got, text)
		}
		if result := store.handle(route, m, time.Now()); result.Status != "ok" {
			t.Fatal(result)
		}
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(read()); got != 3 {
		t.Fatalf("a retry submitted twice: %d", got)
	}
	if err := runRemoteInput(remote.Message{Pane: pane, Kind: "paste", Data: "insert only"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(read()); got != 3 {
		t.Fatalf("Insert unexpectedly submitted: %d", got)
	}
}
