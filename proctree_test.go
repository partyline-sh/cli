package main

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// #814's own verification, made cheap: "SIGSTOP a spawned worker, then assert (a) the per-item
// wall-clock timeout still fires, and (b) teardown leaves no descendant processes."
//
// The incident took seven days to notice; these take under two seconds.

// spawnTree starts a parent that itself spawns a child which outlives it — the shape of a worker
// with MCP servers under it. Both write nothing and sleep well past any test timeout, so anything
// that returns early returned because of teardown, not because the work finished.
func spawnTree(t *testing.T, ctx context.Context, out *bytes.Buffer) *exec.Cmd {
	t.Helper()
	// The inner `sh -c ... &` child deliberately inherits stdout. That inheritance is the whole
	// bug: it holds the pipe write end open, so a Wait that only kills the parent blocks forever.
	cmd := exec.CommandContext(ctx, "sh", "-c", "sh -c 'sleep 60' & sleep 60")
	cmd.Stdout = out
	cmd.Stderr = out
	groupSpawn(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	return cmd
}

func TestTimeoutFiresEvenWhenTheWorkerIsStopped(t *testing.T) {
	// THE regression test. A stopped worker used to block the very timer meant to bound it, which
	// is how one stayed resident for seven days. If this ever hangs instead of failing, that is the
	// bug back — the test timeout is the backstop.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	var out bytes.Buffer
	cmd := spawnTree(t, ctx, &out)
	pid := cmd.Process.Pid

	// Freeze the whole group, exactly as SIGTTIN would have.
	if err := syscall.Kill(-pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP the group: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		if ctx.Err() != context.DeadlineExceeded {
			t.Fatalf("returned before the deadline — the fixture isn't exercising the timeout")
		}
	case <-time.After(waitDelay + killGrace + 5*time.Second):
		t.Fatal("Wait never returned on a STOPPED worker — the timeout cannot fire (#814)")
	}
	// Belt and braces for the test's own hygiene, whatever the outcome above.
	terminateGroup(pid)
}

func TestTeardownLeavesNoDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
	cmd := spawnTree(t, ctx, &out)
	pid := cmd.Process.Pid

	// Let the inner child actually exist before tearing down — otherwise the test could pass by
	// racing ahead of the thing it is supposed to kill.
	waitFor(t, 2*time.Second, func() bool { return len(descendantPIDs(t, pid)) >= 1 })
	kids := descendantPIDs(t, pid)
	if len(kids) == 0 {
		t.Fatal("fixture never spawned a child — nothing to orphan")
	}

	// Stopped, because that is the case where a naive SIGTERM does nothing: a stopped process never
	// runs its handler. Without the SIGCONT in terminateGroup this assertion fails.
	_ = syscall.Kill(-pid, syscall.SIGSTOP)

	terminateGroup(pid)
	go func() { _ = cmd.Wait() }() // reap, so the parent doesn't linger as a zombie in the count

	// Assert the RECORDED CHILD PIDS are gone, not merely that the group is unreachable. Those are
	// different claims and the difference is the whole bug: with no process group, kill(-pid) fails
	// with ESRCH and a groupAlive check reports "dead" while eight MCP servers are still resident.
	// A test that accepts ESRCH as success passes on exactly the broken code it exists to catch.
	waitFor(t, killGrace+5*time.Second, func() bool { return noneAlive(kids) })
	if alive := stillAlive(kids); len(alive) > 0 {
		t.Fatalf("descendants survived teardown: %v — this is the orphaned MCP tree (#814)", alive)
	}
}

// stillAlive returns the subset of pids that still exist. Signal 0 checks existence + permission
// without delivering anything.
func stillAlive(pids []int) []int {
	var out []int
	for _, p := range pids {
		if syscall.Kill(p, 0) == nil {
			out = append(out, p)
		}
	}
	return out
}

func noneAlive(pids []int) bool { return len(stillAlive(pids)) == 0 }

func TestGroupSpawnPutsTheWorkerInItsOwnGroup(t *testing.T) {
	// The property everything else rests on. If the worker shares OUR group, `kill(-pgid)` during
	// teardown would signal the test binary (and in production, crank itself) — so this is a safety
	// assertion as much as a behavioural one.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var out bytes.Buffer
	cmd := spawnTree(t, ctx, &out)
	defer terminateGroup(cmd.Process.Pid)

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Fatalf("worker pgid %d != its pid %d — not its own group", pgid, cmd.Process.Pid)
	}
	if pgid == syscall.Getpgrp() {
		t.Fatal("worker shares OUR process group — a group kill would take us with it")
	}
}

// descendantPIDs lists processes whose parent is pid. `pgrep -P` is in reach on both darwin and
// linux, which is where this runs.
func descendantPIDs(t *testing.T, pid int) []int {
	t.Helper()
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	var pids []int
	for _, line := range strings.Fields(strings.TrimSpace(string(out))) {
		if n, err := strconv.Atoi(line); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
