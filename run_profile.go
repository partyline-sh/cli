package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/gitwt"
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
	// VisualVerify is the web's per-project T2d TOGGLE (safe control-plane data, never a script):
	// when true resolveRun appends --visual so crank runs the visual gate for this run.
	VisualVerify bool
	// VisualRoutes are SAFE render DATA (app paths to screenshot) for the daemon's framework preset.
	// Each is validated against visualRouteRe and written to a daemon-controlled file (never argv),
	// exactly like the worklist — a route string can never smuggle a flag or become a command.
	VisualRoutes []string
}

// visualRouteRe pins a screenshot route to a strict, unambiguously-safe app-path shape: it MUST
// start with "/" (so a leading "-" can't smuggle a flag), and may contain only path/query chars —
// no whitespace, quotes, backticks, "$", ";", "|", "&"-as-shell, "..", or newlines. Anything a
// shell or the argv could interpret simply fails to validate and is dropped. This is the daemon-
// side gate on web-supplied route DATA; the preset (visual.go) quotes them again as defense-in-depth.
var visualRouteRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*(\?[A-Za-z0-9._~=&%/-]*)?$`)

// safeVisualRoutes drops any web-supplied route that isn't an unambiguously-safe app path (see
// visualRouteRe) and rejects "." / ".." path segments outright. Returns only the survivors — an
// all-invalid set yields nil, so the preset falls back to its default route rather than erroring.
func safeVisualRoutes(routes []string) []string {
	var ok []string
	for _, r := range routes {
		r = strings.TrimSpace(r)
		if !visualRouteRe.MatchString(r) {
			continue
		}
		bad := false
		for _, seg := range strings.Split(r, "/") {
			if seg == "." || seg == ".." {
				bad = true
				break
			}
		}
		if !bad {
			ok = append(ok, r)
		}
	}
	return ok
}

// threadIDRe pins a thread id to a strict uuid/slug shape: it must START with an alnum (so a
// leading "-" can't smuggle a flag) and contain only alnum, "_" or "-". That rejects "..",
// "/", ";", whitespace, and newlines outright — anything that could escape into a path or an
// argv fragment simply fails to validate.
var threadIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// runPresetAllowsBash maps a run preset to the worker tool posture. The code-implementation presets
// — "build" and "spec" (the DEFAULT for code tasks, web runs.ts) — grant Bash so the worker can
// actually verify its change (run tests/typecheck) before the PR; without it the agent can't verify
// AND burns enormous tokens discovering the restriction by trial (see workerBashPosture). Any other
// preset (empty/unknown/non-code) falls through to the restrictive no-Bash default — crank then runs
// its read/edit-only allowlist. --dangerously-skip-permissions is NEVER an option regardless.
func runPresetAllowsBash(preset string) bool {
	switch strings.TrimSpace(strings.ToLower(preset)) {
	case "build", "spec":
		return true
	default:
		return false
	}
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

// writeRunSkills stages the run's ENABLED org skills (fetched at launch) as skills.json in a per-run,
// DAEMON-controlled dir, and returns that dir for crank's --skills-dir. Like writeRunGlobals, the path
// is derived only from the already-validated run id (never from skill content) so the skill set is pure
// DATA. crank re-validates every skill NAME before it becomes a path (gitwt.MaterializeSkills).
//
// Packaged skills also stage their zip as <dir>/<name>.zip (crank unpacks it into each worktree). The
// zip FILENAME is a skill name, itself a path-injection vector, so it is re-validated with the same
// slug rule before it becomes a path — an invalid name is skipped (that skill degrades to body-only).
func writeRunSkills(runID string, skills []api.Skill, bundles map[string][]byte) (string, error) {
	dir := filepath.Join(stateDir(), "daemon", "skills", runID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.Marshal(skills)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "skills.json"), b, 0o600); err != nil {
		return "", err
	}
	for name, zip := range bundles {
		if !gitwt.ValidSkillName(name) || len(zip) == 0 {
			continue // untrusted name never becomes a path; empty zip has nothing to stage
		}
		if err := os.WriteFile(filepath.Join(dir, name+".zip"), zip, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "  (skill %q: bundle stage failed: %v — will inject body-only)\n", name, err)
		}
	}
	return dir, nil
}

// writeRunVisualRoutes materializes the ALREADY-VALIDATED screenshot routes as crank's
// --visual-routes input, one per line, in the same DAEMON-controlled dir as the worklist (never
// the project dir; filename derived only from the validated thread id). Routes reach crank as
// DATA, never argv — so a route can't add a flag. Returns "" (no file) when there are no routes.
func writeRunVisualRoutes(threadID string, routes []string) (string, error) {
	if len(routes) == 0 {
		return "", nil
	}
	dir := filepath.Join(stateDir(), "daemon", "worklists")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, threadID+".routes.txt")
	if err := os.WriteFile(path, []byte(strings.Join(routes, "\n")+"\n"), 0o600); err != nil {
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
	if fi, e := os.Stat(proj.Path); e != nil || !fi.IsDir() {
		return nil, "", fmt.Errorf("project dir is gone: %s", proj.Path)
	}
	argv, err = buildRunArgv(ref)
	if err != nil {
		return nil, "", err
	}
	return argv, proj.Path, nil
}

// buildRunArgv builds the FIXED crank argv for a runRef — the part shared by registry resolution
// (resolveRun) and clone-on-demand resolution (provisionRun). It validates the thread id, requires at
// least one task, writes the worklist (+ optional visual routes) as daemon-controlled DATA files, and
// returns the argv. The working DIR is chosen by the CALLER (a registry path, or a managed clone) and
// is NEVER derived from any ref field — so the "no ref field becomes a path or a flag" invariant holds
// for both callers. On any rejection it returns nil argv + the error.
func buildRunArgv(ref runRef) (argv []string, err error) {
	if !threadIDRe.MatchString(ref.ThreadID) {
		return nil, fmt.Errorf("invalid thread id %q", ref.ThreadID)
	}
	if len(ref.Tasks) == 0 {
		return nil, fmt.Errorf("run profile has no tasks")
	}
	worklist, err := writeRunWorklist(ref.ThreadID, ref.Tasks)
	if err != nil {
		return nil, fmt.Errorf("worklist: %w", err)
	}
	argv = []string{"crank", "--file", worklist, "--thread", ref.ThreadID}
	if runPresetAllowsBash(ref.Preset) {
		argv = append(argv, "--allow-bash") // else crank's restrictive, no-Bash default
	}
	// T2d visual verify (web toggle). --visual is a FIXED flag (never web text); routes are the
	// only variable input and reach crank as a daemon-written DATA file, exactly like the worklist.
	// The web supplies the toggle + route data only — never the render script (crank resolves that
	// from the repo-trusted `.partyline/visual` or a daemon-hardcoded preset).
	if ref.VisualVerify {
		argv = append(argv, "--visual")
		routesFile, rerr := writeRunVisualRoutes(ref.ThreadID, safeVisualRoutes(ref.VisualRoutes))
		if rerr != nil {
			return nil, fmt.Errorf("visual routes: %w", rerr)
		}
		if routesFile != "" {
			argv = append(argv, "--visual-routes", routesFile)
		}
	}
	return argv, nil
}
