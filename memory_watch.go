package main

// LIVE UPDATES, in three layers, because only one of them can be assumed to exist.
//
//  1. THIS MACHINE, INSTANTLY. Anything that writes a fact pokes the status bar the moment it
//     lands (pokeStatus). No polling, no network, no latency worth measuring.
//
//  2. A TEAMMATE'S MACHINE, NEAR-LIVE AND WITHOUT INFRASTRUCTURE. A watcher fetches each
//     project's memory repo on an interval, off the render path, and pokes the bar only when the
//     remote ref actually moved. This is the layer that always works — no account, no server, no
//     open port, behind any firewall.
//
//  3. A TEAMMATE'S MACHINE, INSTANTLY. When a partyline instance is in play it can push a
//     "memory changed" event over its existing stream and the watcher fetches on the event rather
//     than the clock. That is a strict accelerator on top of layer 2, never a replacement: the
//     moment it becomes the only path, a team without a server has no live updates at all, and a
//     server outage silently freezes everyone's memory instead of slowing it down.
//
// Layers 1 and 2 are implemented here. The interval deliberately trades latency for silence: a
// fetch is cheap but not free, and nothing in a shared-memory system is urgent to the second.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const memoryWatchDefault = 45 * time.Second

// pokeStatus forces an immediate status-bar redraw. Best-effort and silent: no tmux server (a
// plain terminal, a CI run) is a perfectly ordinary state, not a failure.
func pokeStatus() {
	if _, err := os.Stat(filepath.Join(stateDir(), "tmux.conf")); err != nil {
		return
	}
	_ = tmuxCmd("refresh-client", "-S").Run()
}

// memoryWatchMain is `ptln memory watch` (hidden): one process per machine, watching every
// project this machine has, so a teammate's fact shows up in the bar without anyone asking.
func memoryWatchMain(args []string) {
	every := memoryWatchDefault
	for i := 0; i < len(args); i++ {
		if args[i] == "--every" && i+1 < len(args) {
			if d, err := time.ParseDuration(args[i+1]); err == nil && d >= 5*time.Second {
				every = d
			}
			i++
		}
	}
	// One watcher per machine. A second one would double the fetch traffic and poke the same bar
	// twice; the lock is how re-sourcing the tmux conf (which happens on every launch) stays
	// idempotent.
	lock := filepath.Join(stateDir(), "memory-watch.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) < 10*time.Minute {
			return // a healthy watcher already has it
		}
		_ = os.Remove(lock)
		if f, err = os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
			return
		}
	}
	_ = f.Close()
	defer os.Remove(lock)

	// The bus, when there is one, turns the interval below into a backstop rather than the
	// delivery mechanism. It is started AFTER the lock is taken so there is one listener per
	// machine, and it is never waited on — see memory_bus.go.
	ctx, stopBus := context.WithCancel(context.Background())
	defer stopBus()
	go watchBus(ctx)

	for {
		for _, ws := range allWorkspaces() {
			if memoryFetchMoved(ws.Dir) {
				pokeStatus()
			}
		}
		// Touch the lock so a watcher that is alive keeps its claim, and one that died releases
		// it by going stale rather than wedging the next launch forever.
		_ = os.Chtimes(lock, time.Now(), time.Now())
		time.Sleep(every)
	}
}

// allWorkspaces is every project this machine has joined.
func allWorkspaces() []*projectWorkspace {
	entries, err := os.ReadDir(projectsRoot())
	if err != nil {
		return nil
	}
	var out []*projectWorkspace
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ws, err := loadProjectWorkspace(e.Name()); err == nil {
			out = append(out, ws)
		}
	}
	return out
}

// memoryFetchMoved fetches and reports whether the remote actually moved, so the bar is poked on
// news rather than on a timer. Fast-forwards when it did: a memory that is fetched but not merged
// would show "new" forever and brief nothing.
func memoryFetchMoved(dir string) bool {
	if !hasUpstream(dir) {
		return false
	}
	before, _ := gitMem(dir, "rev-parse", "@{u}")
	if _, err := gitMem(dir, "fetch", "--quiet"); err != nil {
		return false
	}
	after, _ := gitMem(dir, "rev-parse", "@{u}")
	if strings.TrimSpace(before) == strings.TrimSpace(after) {
		return false
	}
	if _, err := memoryPull(dir); err != nil {
		// Diverged (both sides recorded and neither has pulled) — say it in the bar rather than
		// merging behind the human's back.
		fmt.Fprintln(os.Stderr, "partyline: memory in", dir, "needs attention:", err)
	}
	return true
}
