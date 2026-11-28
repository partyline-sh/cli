package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestChatCopyUsesTmuxClipboardPath(t *testing.T) {
	const socket = "ptln-test-chat-clipboard"
	tmuxTestServer(t, socket)
	t.Setenv("TERM", "xterm-256color")
	openTagged(t, Spec{Label: "copy fixture", Key: "copy-fixture", Argv: []string{"sleep", "300"}})
	// Observe the client terminal itself: a tmux buffer alone does not prove
	// that clipboard bytes escaped the popup and reached the SSH terminal.
	if err := tmuxCmd("set-option", "-as", "terminal-features", ",xterm*:clipboard").Run(); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "terminal-output")
	attachChatClipboardClient(t, trace)
	t.Setenv("TMUX", "/tmp/"+socket+",1,0")
	before := chatClipboardWrite
	chatClipboardWrite = func(string) (bool, error) {
		t.Fatal("used server's native clipboard for an SSH client")
		return false, nil
	}
	t.Cleanup(func() { chatClipboardWrite = before })
	text := "selected text\nUnicode: 界"
	result, ok := chatCopyText(text)().(chatCopied)
	if !ok || result.err != nil || !result.tmux {
		t.Fatalf("copy result: %+v", result)
	}
	data, err := tmuxCmd("save-buffer", "-").Output()
	if err != nil || string(data) != text {
		t.Fatalf("tmux buffer differs: %q, %v", data, err)
	}
	if _, err := os.Stat(copyToastPath()); err != nil {
		t.Fatal("session copy confirmation wasn't updated")
	}
	want := base64.StdEncoding.EncodeToString([]byte(text))
	for i := 0; i < 100; i++ {
		data, _ := os.ReadFile(trace)
		if strings.Contains(string(data), "\x1b]52;") && strings.Contains(string(data), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("selection did not reach the terminal clipboard escape")
}

func attachChatClipboardClient(t *testing.T, trace string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	attach := tmuxCmd("attach-session", "-t", tmuxSessionName)
	script := `import os,pty,signal,sys
os.environ.pop("TMUX",None)
trace=sys.argv[1]
pid,fd=pty.fork()
if pid==0: os.execvp(sys.argv[2],sys.argv[2:])
signal.signal(signal.SIGTERM,lambda *_: sys.exit(0))
try:
 with open(trace,"ab",buffering=0) as f:
  while True:
   data=os.read(fd,65536)
   if not data: break
   f.write(data)
finally:
 try: os.kill(pid,signal.SIGTERM)
 except ProcessLookupError: pass
 os.waitpid(pid,0)
`
	cmd := exec.Command(python, append([]string{"-c", script, trace}, attach.Args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); _ = cmd.Wait() })
	for i := 0; i < 100; i++ {
		clients, _ := tmuxCmd("list-clients", "-F", "#{client_name}").Output()
		if strings.TrimSpace(string(clients)) != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("clipboard test client did not attach")
}

func TestChatCopyFailureDoesNotClaimClipboardSuccess(t *testing.T) {
	m := seededChat(t, 0)
	next, _ := m.Update(chatCopied{lines: 1, err: os.ErrPermission})
	if text := next.(chatPopup).copied; !strings.Contains(text, "copy failed") || strings.Contains(text, "sent") {
		t.Fatalf("misleading copy result: %q", text)
	}
}
