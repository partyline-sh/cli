package main

// The share tap: what lets `ptln` share a tmux-hosted session over the SAME relay transport
// as the built-in mux. The relay host needs a *ptysess.Session (participant hub, vt replay,
// lock/roster). Rather than reimplement that hub over tmux, the share creates a REAL
// ptysess.Session whose child is `ptln tmux --tap <pane>`: a process that prints the pane's
// current screen (colors included) and then streams every byte the pane emits, via
// tmux pipe-pane through a private fifo. The Session's vt builds from that stream exactly as
// it would from a spawned program, so late joiners, replay, HUD — all of it — just works.
//
// One pipe per pane is a tmux rule; the tap owns it while running and switches it off on the
// way out (signal or pane death).

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func tmuxTap(pane string, w io.Writer, stop <-chan struct{}) error {
	// current screen first — a joiner attaching mid-session must not start from black
	if snap, err := tmuxCmd("capture-pane", "-e", "-p", "-t", pane).Output(); err == nil {
		_, _ = w.Write(snap)
	}

	dir, err := os.MkdirTemp("", "ptln-tap-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	fifo := filepath.Join(dir, "out")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		return err
	}
	// order matters: pipe-pane's `cat` blocks opening the fifo for write until we open the
	// read side, so start the pipe first, then open — both unblock together.
	if out, err := tmuxCmd("pipe-pane", "-t", pane, "-o", "cat >> "+shQuote(fifo)).CombinedOutput(); err != nil {
		return fmt.Errorf("pipe-pane: %v\n%s", err, out)
	}
	defer func() { _ = tmuxCmd("pipe-pane", "-t", pane).Run() }() // off
	f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(w, f) // pane died or pipe switched off → EOF
		close(done)
	}()
	select {
	case <-done:
	case <-stop:
	}
	return nil
}

// tmuxTapMain is the --tap entry: stream to stdout until the pane ends or we're told to stop.
func tmuxTapMain(pane string) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	stop := make(chan struct{})
	go func() { <-sig; close(stop) }()
	if err := tmuxTap(pane, os.Stdout, stop); err != nil {
		fmt.Fprintln(os.Stderr, "ptln tmux --tap:", err)
		os.Exit(1)
	}
}
