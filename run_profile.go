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
	// RunID scopes the worklist file to ONE run. It is not optional bookkeeping: the worklist used
	// to be named by THREAD id, and every run in a thread therefore wrote and read the same file.
	// With two runs executing concurrently on a machine — the daemon's default — run B overwrote
	// run A's worklist before A's crank read it, and A silently built B's task.
	//
	// Observed in prod 2026-08-15: run ef6b8616 ("Stop the queued-run banner…") executed the text
	// "Review the changes this run produced against its task." — the placeholder from a REVIEW run
	// in the same thread. It branched, ran and reported against the wrong task, and because the
	// wrong task had nothing to do it finished "no changes · no commit" and looked like a clean
	// no-op. That is the dangerous shape: not a crash, a plausible success for work never done.
	RunID  string
	Tasks  []string
	Preset string
	// VisualVerify is the web's per-project T2d TOGGLE (safe control-plane data, never a script):
	// when true resolveRun appends --visual so crank runs the visual gate for this run.
	VisualVerify bool
	// VisualRoutes are SAFE render DATA (app paths to screenshot) for the daemon's framework preset.
	// Each is validated against visualRouteRe and written to a daemon-controlled file (never argv),
	// exactly like the worklist — a route string can never smuggle a flag or become a command.
	VisualRoutes []string
	// Checks and ReviewLanes are the project's PIPELINE POLICY (G.6) — which named checks run and
	// whether they block, and which reviewer engines judge the diff. Same posture as VisualRoutes
	// and for the same reason: policy DATA written to a daemon-controlled file, never argv, never a
	// command. crank re-validates every field (readPipelineFile) rather than trusting what we wrote,
	// and applyPolicy drops any name the repo does not itself declare — so the worst a hostile
	// control plane can do here is switch OFF a check, which is visible in the gate report.
	Checks      []api.RunCheckPolicy
	ReviewLanes []api.RunReviewLane
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
// (never the project dir — the repo stays clean, and the filename is derived only from an
// already-validated id, never from task content). Embedded newlines are collapsed so a task can't
// inject extra worklist lines; either way tasks are only ever DATA to crank.
//
// `name` MUST be unique per run. It was the thread id, which is shared by every run in a thread —
// see runRef.RunID for what that cost. Falls back to the thread id only when no run id is present
// (a hand-driven path with nothing concurrent), and both are regex-validated by the caller.
func writeRunWorklist(name string, tasks []string) (string, error) {
	dir := filepath.Join(daemonDir(), "worklists")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".txt")
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
	dir := filepath.Join(daemonDir(), "globals")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, runID+".md")
	if err := os.WriteFile(path, []byte(globals), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// writeRunGrants materializes the run's build-role tool grants (#575) as JSON crank reads via
// --grants-file, in a DAEMON-controlled dir. Same posture as writeRunGlobals: the filename derives
// only from the already-validated run id, and the content is pure DATA — the worker re-validates
// every entry (resolveLaunchGrants) before anything widens.
func writeRunGrants(runID string, g *api.ToolGrants) (string, error) {
	dir := filepath.Join(daemonDir(), "grants")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, runID+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
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
	dir := filepath.Join(daemonDir(), "skills", runID)
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
	dir := filepath.Join(daemonDir(), "worklists")
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
	// Scope the worklist to the RUN, falling back to the thread only when there is no run id.
	// Validated here rather than trusted: this string becomes a filename.
	worklistName := ref.ThreadID
	if ref.RunID != "" {
		if !runIDRe.MatchString(ref.RunID) {
			return nil, fmt.Errorf("invalid run id %q", ref.RunID)
		}
		worklistName = ref.RunID
	}
	worklist, err := writeRunWorklist(worklistName, ref.Tasks)
	if err != nil {
		return nil, fmt.Errorf("worklist: %w", err)
	}
	// --literal: a daemon-written worklist is DATA, so every line is a task. Task titles legitimately
	// start with "#" (an issue ref — "#570: ..."), and without this crank stripped them as comments,
	// found zero tasks, and the run reported `done` having built nothing (chain c842d926).
	argv = []string{"crank", "--file", worklist, "--thread", ref.ThreadID, "--literal"}
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
	// G.6 pipeline policy. Written as one DATA file for the same reason the worklist is: a policy
	// value must never become an argv fragment. An unwritable file is NOT fatal — the run proceeds
	// on the DEFAULT pipeline (every check blocking, one reviewer), which is the strict direction.
	// Failing the run instead would mean a disk hiccup in settings delivery stops the build.
	if pf, perr := writeRunPipeline(ref.ThreadID, ref.Checks, ref.ReviewLanes); perr == nil && pf != "" {
		argv = append(argv, "--pipeline", pf)
	}
	return argv, nil
}

// writeRunPipeline materializes the project's check policy + reviewer lanes as crank's --pipeline
// input, in the same DAEMON-controlled dir as the worklist (filename derived only from the already-
// validated thread id, never from a policy field). Returns "" when the project set no policy, which
// is the common case and means crank runs its default pipeline.
func writeRunPipeline(threadID string, checks []api.RunCheckPolicy, lanes []api.RunReviewLane) (string, error) {
	if len(checks) == 0 && len(lanes) == 0 {
		return "", nil
	}
	body, err := json.Marshal(pipelineFile{
		Checks: toCheckPolicies(checks),
		Lanes:  toLanePolicies(lanes),
	})
	if err != nil {
		return "", err
	}
	dir := filepath.Join(daemonDir(), "worklists")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, threadID+".pipeline.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func toCheckPolicies(in []api.RunCheckPolicy) []checkPolicy {
	out := make([]checkPolicy, 0, len(in))
	for _, c := range in {
		out = append(out, checkPolicy{Name: c.Name, Enabled: c.Enabled, Blocking: c.Blocking, PathGlob: c.PathGlob})
	}
	return out
}

func toLanePolicies(in []api.RunReviewLane) []lanePolicy {
	out := make([]lanePolicy, 0, len(in))
	for _, l := range in {
		out = append(out, lanePolicy{ID: l.ID, Engine: l.Engine, Model: l.Model})
	}
	return out
}
