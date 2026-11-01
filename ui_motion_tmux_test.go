package main

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A real attached client on a private socket: no focus setting changes to live agents.
func TestMotionTmuxVisibility(t *testing.T) {
	tmuxTestServer(t, "ptln-test-motion-visibility")
	t.Setenv("TERM", "xterm-256color")
	pane := openTagged(t, Spec{Label: "motion test", Key: "motion-test", Argv: []string{"sleep", "300"}})
	t.Setenv("TMUX_PANE", pane)
	if motionPaneVisible() {
		t.Fatal("detached session animates")
	}
	attachPrivateTmuxClient(t)
	visible := func(want bool) {
		t.Helper()
		for i := 0; i < 50; i++ {
			if motionPaneVisible() == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("visibility want %v", want)
	}
	visible(true)
	if err := tmuxCmd("new-window", "-t", tmuxSessionName, "--", "sleep", "300").Run(); err != nil {
		t.Fatal(err)
	}
	visible(false)
	if err := tmuxCmd("select-window", "-t", windowOf(t, pane)).Run(); err != nil {
		t.Fatal(err)
	}
	visible(true)
	if err := tmuxCmd("split-window", "-h", "-t", pane, "--", "sleep", "300").Run(); err != nil {
		t.Fatal(err)
	}
	visible(false)
	if err := tmuxCmd("select-pane", "-t", pane).Run(); err != nil {
		t.Fatal(err)
	}
	visible(true)
}

func attachPrivateTmuxClient(t *testing.T) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	attach := tmuxCmd("attach-session", "-t", tmuxSessionName)
	script := `import os,pty,signal,sys
os.environ.pop("TMUX",None)
pid,fd=pty.fork()
if pid==0: os.execvp(sys.argv[1],sys.argv[1:])
signal.signal(signal.SIGTERM,lambda *_: sys.exit(0))
try:
 while os.read(fd,65536): pass
finally:
 try: os.kill(pid,signal.SIGTERM)
 except ProcessLookupError: pass
 os.waitpid(pid,0)
`
	cmd := exec.Command(python, append([]string{"-c", script}, attach.Args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); _ = cmd.Wait() })
	for i := 0; i < 50; i++ {
		if clients, err := tmuxCmd("list-clients", "-F", "#{client_name}").Output(); err == nil && strings.TrimSpace(string(clients)) != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("private terminal client did not attach")
}
