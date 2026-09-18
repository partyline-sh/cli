package main

import (
	"os/exec"
	"syscall"
	"time"
)

// Spawning an engine worker so that it can always be torn down (#814).
//
// THE INCIDENT. A crank worker was found SIGSTOP'd (state `T`) for 7 days 13 hours, holding eight
// orphaned MCP servers, invisible to every partyline surface. `kill <pid>` killed one process; its
// children survived, reparented to launchd. Nine processes, a week of memory, unreachable.
//
// TWO DEFECTS, ONE FIX.
//
//  1. The wall-clock timeout could not fire. This is the non-obvious one, and it is NOT (as it
//     first appears) that a stopped process ignores signals — SIGKILL is delivered to a stopped
//     process just fine. It is that `cmd.Run()` / `cmd.Wait()` waits for the output PIPES to reach
//     EOF, and the worker's MCP children INHERIT the write end. Kill the worker and the pipe stays
//     open in eight grandchildren, so Wait blocks forever on a process that already died. The timer
//     meant to bound the task is itself bounded by the task.
//
//  2. Teardown killed one process, not the tree. The daemon already gets this right for runs —
//     killRun signals the GROUP (`-pid`) and SIGCONTs after SIGTERM, because a stopped process
//     never runs its handler so a pending SIGTERM just sits there. Crank workers simply never got
//     the same treatment.
//
// Killing the GROUP fixes both: the grandchildren die, which closes the inherited pipe write ends,
// which lets Wait return. So there is no new mechanism here — it is killRun's already-reasoned
// discipline applied to the second call site, plus stdlib's WaitDelay as a backstop for anything
// that still holds a descriptor.

// waitDelay bounds how long Wait blocks on output pipes AFTER the process itself is gone. Without
// it, one un-signalled descendant holding the write end hangs the worker forever — the exact
// failure above. Generous enough that a healthy worker's final flush is never truncated.
const waitDelay = 10 * time.Second

// killGrace is how long a group gets to exit on SIGTERM before SIGKILL. Short: this path only runs
// when the task is already over budget or being torn down, and a worker that ignores SIGTERM is
// precisely the case that produced a week-long orphan.
const killGrace = 5 * time.Second

// groupSpawn configures cmd so it can be killed as a tree. Call it BEFORE Start/Run.
//
// Setpgid gives the worker its own process group, which does two things: it makes `kill(-pgid)`
// reach every descendant that has not deliberately left the group, and it detaches the worker from
// the launching terminal's foreground group — so a headless `claude -p` can no longer take
// SIGTTIN/SIGTTOU and stop itself, which is the suspected origin of the incident. Fixing the cause,
// not just reaping the symptom.
func groupSpawn(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	// Overrides exec.CommandContext's default cancel, which sends Kill to the PROCESS only and
	// leaves the tree standing. With Setpgid the group id equals the child's pid.
	cmd.Cancel = func() error {
		terminateGroup(cmd.Process.Pid)
		return nil
	}
	cmd.WaitDelay = waitDelay
}

// terminateGroup asks a whole process group to exit, then makes sure it can.
//
// The SIGCONT is the part that is easy to omit and fatal to omit: a SIGSTOP'd process never runs
// its signal handler, so the SIGTERM merely queues and the group would die only to a later SIGKILL
// — while its children, never signalled at all, would not die even then. Continuing the group lets
// it wake, observe the SIGTERM, and exit cleanly. Harmless for a group that was already running.
//
// Best-effort throughout: every failure mode here (already reaped, not ours, permission) means the
// group is not our problem any more, and an error return would only invite a caller to retry
// something that cannot succeed.
func terminateGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(-pid, syscall.SIGCONT)

	// SIGKILL the group after the grace period, unless it has already gone. Done in the background
	// so teardown never blocks the caller: the point of this path is to STOP waiting on a worker.
	go func() {
		time.Sleep(killGrace)
		if groupAlive(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}()
}

// groupAlive reports whether any process remains in the group. Signal 0 performs the permission and
// existence checks without delivering anything — the standard liveness probe.
func groupAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(-pid, 0) == nil
}
