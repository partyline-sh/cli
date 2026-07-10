package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// run_profile.go — EPIC O (O.1): the run-profile REFERENCE + its resolver, the widened
// sibling of resolveLaunch. The web will later enqueue a runRef (owner ORCHESTRATION
// INTENT: which project, which thread, which tasks, which preset); the daemon reconciles it
// against its LOCAL registry and drives `crank`. This slice ONLY defines + secures that
// reference — no web endpoint, stream, or reconcile is wired here.
//
// SECURITY INVARIANT (identical to resolveLaunch — see daemon.go): the reference is DATA,
// never a command. resolveRun is the SOLE chokepoint. The project label is matched EXACTLY
// against the owner-authored registry; the working dir is the matched project's registry
// Path and NOTHING else; no field of the reference is ever concatenated into the dir or
// used as an argv flag/fragment. Tasks reach crank only as DATA (a worklist file crank reads
// via --file), so a task string carrying "\n--dangerously-skip-permissions", backticks, or
// "$(...)" can neither add a flag nor change the argv. --dangerously-skip-permissions is
// never emitted; the tool allowlist defaults to the most restrictive (no Bash).

// runRef is what the control plane will enqueue: a reference, never a command. Tasks are the
// plain worklist strings (owner intent); they are passed to crank as data, never as argv.
type runRef struct {
	ProjectLabel string
	ThreadID     string
	Tasks        []string
	Preset       string
}

// threadIDRe pins a thread id to a strict uuid/slug shape: it must START with an alnum (so a
// leading "-" can't smuggle a flag) and contain only alnum, "_" or "-". That rejects "..",
// "/", ";", whitespace, and newlines outright — anything that could escape into a path or an
// argv fragment simply fails to validate.
var threadIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// runPresetAllowsBash maps a run preset to the worker tool posture. Only the explicit "build"
// preset grants Bash; empty/unknown falls through to the restrictive default (no Bash — crank
// then runs its read/edit-only allowlist). --dangerously-skip-permissions is NEVER an option.
func runPresetAllowsBash(preset string) bool {
	return strings.TrimSpace(strings.ToLower(preset)) == "build"
}

// writeRunWorklist materializes the tasks as crank's --file input, in a DAEMON-controlled dir
// (never the project dir — the repo stays clean, and the filename is derived only from the
// already-validated thread id, never from task content). Embedded newlines are collapsed so a
// task can't inject extra worklist lines; either way tasks are only ever DATA to crank.
func writeRunWorklist(threadID string, tasks []string) (string, error) {
	dir := filepath.Join(stateDir(), "daemon", "worklists")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, threadID+".txt")
	var b strings.Builder
	for _, t := range tasks {
		line := strings.ReplaceAll(strings.ReplaceAll(t, "\r", " "), "\n", " ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// writeRunGlobals materializes the project's globals document (Phase B3) as a file crank reads via
// --globals-file, in a DAEMON-controlled dir (never the project dir). Filename is derived only from
// the already-validated run id (runIDRe, checked in augmentRunArgv before this is called), never from
// the document content — so the doc is pure DATA, exactly like the worklist.
func writeRunGlobals(runID, globals string) (string, error) {
	dir := filepath.Join(stateDir(), "daemon", "globals")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, runID+".md")
	if err := os.WriteFile(path, []byte(globals), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// resolveRun is the ONLY place a runRef becomes a runnable command — the widened twin of
// resolveLaunch. Exact-match the label (injection can't widen scope), validate the thread id,
// require at least one task, verify the registered dir still exists, write the worklist as
// data, and build a FIXED crank argv whose only variable inputs are the daemon-controlled
// worklist path + the validated thread id. Returns the argv + the registry dir, or an error
// (and NO argv) on any rejection.
func resolveRun(reg daemonRegistry, ref runRef) (argv []string, dir string, err error) {
	proj := projectByLabel(reg, ref.ProjectLabel)
	if proj == nil {
		return nil, "", fmt.Errorf("unknown project %q — not in the local registry", ref.ProjectLabel)
	}
	if !threadIDRe.MatchString(ref.ThreadID) {
		return nil, "", fmt.Errorf("invalid thread id %q", ref.ThreadID)
	}
	if len(ref.Tasks) == 0 {
		return nil, "", fmt.Errorf("run profile has no tasks")
	}
	if fi, e := os.Stat(proj.Path); e != nil || !fi.IsDir() {
		return nil, "", fmt.Errorf("project dir is gone: %s", proj.Path)
	}
	worklist, err := writeRunWorklist(ref.ThreadID, ref.Tasks)
	if err != nil {
		return nil, "", fmt.Errorf("worklist: %w", err)
	}
	argv = []string{"crank", "--file", worklist, "--thread", ref.ThreadID}
	if runPresetAllowsBash(ref.Preset) {
		argv = append(argv, "--allow-bash") // else crank's restrictive, no-Bash default
	}
	return argv, proj.Path, nil
}
