package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// assignproject.go — executing a destination assignment: match the handle back to a directory this
// machine offered, then EITHER register a checkout that is already there OR clone into a directory
// that isn't, reporting every transition. The advertising half (and the security posture both
// halves share) is destinations.go.

// destCloneTimeout bounds the clone. Long enough for a big repo on a slow link, short enough that a
// wedged credential prompt or a dead host cannot leave an assignment "cloning" forever.
const destCloneTimeout = 15 * time.Minute

// cloneURLRe / scpURLRe are the ALLOW-LIST of remote forms that may reach `git clone`. Anything
// else — a local path, file://, git's `ext::<command>` transport (arbitrary command execution), a
// leading dash, whitespace, a shell metacharacter — is refused before argv exists.
var (
	cloneURLRe = regexp.MustCompile(`^(?:https|ssh)://[A-Za-z0-9._~:@/%+-]+$`)
	scpURLRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:[A-Za-z0-9._~/+-]+$`)
)

// safeCloneURL validates the one server-supplied string that becomes an argv element.
func safeCloneURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", fmt.Errorf("no repository url in the assignment — nothing to clone")
	}
	if cloneURLRe.MatchString(u) || scpURLRe.MatchString(u) {
		return u, nil
	}
	return "", fmt.Errorf("refusing to clone %q — only https://, ssh:// and git@host:owner/repo remotes are accepted", u)
}

// remoteKey normalizes a git remote to "host/owner/repo" so two spellings of the same repository
// (https vs ssh, with or without .git) compare equal. Used ONLY to decide "is the directory that is
// already here the repo you asked for?" — never to build anything.
func remoteKey(u string) string {
	s := strings.TrimSpace(strings.ToLower(u))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	s = strings.TrimSuffix(s, "/")
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		s = strings.TrimPrefix(s, p)
	}
	if at := strings.LastIndex(s, "@"); at >= 0 { // strip user@ (ssh URLs and scp form alike)
		s = s[at+1:]
	}
	s = strings.Replace(s, ":", "/", 1) // scp form host:owner/repo → host/owner/repo
	return strings.Trim(s, "/")
}

// remoteOriginTimeout bounds the one git call made BEFORE any state is reported. It reads a config
// file, so it is instant — except on a dead network mount, where a stat can block for minutes and
// would strand the assignment before it ever moved off 'queued'.
const remoteOriginTimeout = 15 * time.Second

// remoteOrigin reads a checkout's origin URL. "" when there is none, git is unhappy, or it took too
// long — the caller then treats the directory as "not the repo you asked for", which is the safe
// answer (it refuses rather than overwrites).
func remoteOrigin(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), remoteOriginTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin")
	groupSpawn(cmd) // abandonment kills the GROUP, not just git (#419)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// inspectDestination decides what to do with <parent>/<label> WITHOUT touching the filesystem:
//
//	missing                          → clone into it
//	an existing checkout of this repo → register it, no clone (the machine already has the code)
//	anything else                     → refuse, naming the directory
//
// The refusal is the whole point. Overwriting is never an option here: the directory could be
// someone's work, and an assignment is not consent to delete it.
func inspectDestination(dir, repoURL string) (needClone bool, err error) {
	fi, statErr := os.Stat(dir)
	if os.IsNotExist(statErr) {
		return true, nil
	}
	if statErr != nil {
		return false, fmt.Errorf("can't use %s on this machine (%v)", dir, statErr)
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("%s already exists on this machine and is not a directory — pick another name or destination", dir)
	}
	if isGitRepo(dir) {
		origin := remoteOrigin(dir)
		if origin != "" && remoteKey(origin) == remoteKey(repoURL) {
			return false, nil // already here — register it, never re-clone
		}
		if origin == "" {
			return false, fmt.Errorf("%s is already a git repository with no origin remote — refusing to touch it", dir)
		}
		return false, fmt.Errorf("%s is already a checkout of %s, not %s — refusing to overwrite it", dir, origin, strings.TrimSpace(repoURL))
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		return false, fmt.Errorf("can't read %s on this machine (%v)", dir, readErr)
	}
	if len(entries) > 0 {
		return false, fmt.Errorf("%s already exists on this machine and is not empty — refusing to clone into it", dir)
	}
	return true, nil // an empty directory: git clone is happy to fill it
}

// clearPartialClone undoes a clone that did not finish.
//
// This is not tidiness, it is correctness: a killed clone (timeout, daemon teardown) leaves a
// HALF-WRITTEN CHECKOUT behind, and a partial clone still has a .git directory with the right
// origin in it. inspectDestination would then see "already a checkout of the repo you asked for"
// and the next attempt would register an incomplete working tree as ready — a project that builds
// nothing, with no sign of why. So the whole partial tree goes.
//
// Only ever removes what THIS operation wrote: inspectDestination proved moments earlier that the
// directory was missing or empty. `created` distinguishes the two — we made the directory, so it
// goes; or it existed empty and we merely filled it, so its CONTENTS go and the directory stays.
func clearPartialClone(dir string, created bool) {
	if created {
		_ = os.RemoveAll(dir)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// gitFixHint is the actionable half of a clone failure. Auth for this path is the MACHINE's own git
// — there is no server-supplied credential to blame — so the fix is always something the human does
// on this box, and the message names it rather than leaving them with "exit status 128".
func gitFixHint(repoURL string) string {
	key := remoteKey(repoURL)
	host := key
	if i := strings.Index(key, "/"); i > 0 {
		host = key[:i]
	}
	if host == "github.com" {
		return "run `gh auth login` here (or add this machine's SSH key to GitHub)"
	}
	return "give this machine credentials for " + host + " (an SSH key or a git credential helper)"
}

// envWith returns the environment with the given KEY=VALUE pairs REPLACING any inherited ones —
// appending alone is not enough, since which duplicate a child sees is not something to bet on.
// A pair with an EMPTY value means UNSET (the variable is dropped, not set to ""), because a git
// helper reading an empty GIT_ASKPASS behaves differently from one reading none.
func envWith(pairs ...string) []string {
	drop := map[string]bool{}
	for _, p := range pairs {
		if k, _, ok := strings.Cut(p, "="); ok {
			drop[k] = true
		}
	}
	var env []string
	for _, e := range os.Environ() {
		if k, _, ok := strings.Cut(e, "="); ok && drop[k] {
			continue
		}
		env = append(env, e)
	}
	for _, p := range pairs {
		if _, v, ok := strings.Cut(p, "="); ok && v == "" {
			continue // "KEY=" means unset
		}
		env = append(env, p)
	}
	return env
}

// gitReason condenses a git failure to one legible sentence for the board — the existing
// firstLine (mergepolicy.go) plus a hard clamp, since a clone can fail with a wall of text.
func gitReason(out []byte, err error) string {
	s := firstLine(string(out), err)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// cloneRepo is the clone step, indirected ONLY so tests can exercise the partial-tree cleanup
// below: real git usually tidies up after itself, so the case that matters (a clone KILLED
// mid-write, leaving a .git behind) cannot be produced reliably from the outside.
var cloneRepo = cloneInto

// cloneInto runs the clone with the machine's own credentials, bounded in time and killable as a
// TREE (#419/#814): git spawns ssh/askpass/credential helpers, so cancelling the parent alone
// leaves children holding the output pipe and Wait never returns. groupSpawn puts the clone in its
// own process group and makes the context cancel signal the GROUP.
//
// GIT_TERMINAL_PROMPT=0 and ssh BatchMode turn "waiting for a password nobody will type" into an
// immediate, reportable failure — the difference between a legible refusal and a stuck assignment.
func cloneInto(dir, repoURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), destCloneTimeout)
	defer cancel()
	// No credential is injected: this is the MACHINE's git, so its own ssh key / gh auth /
	// credential helper is exactly what we want it to use.
	cmd := exec.CommandContext(ctx, "git", "clone", "--", repoURL, dir)
	cmd.Env = envWith(
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -oBatchMode=yes -oStrictHostKeyChecking=accept-new",
		"GIT_ASKPASS=", "SSH_ASKPASS=",
	)
	groupSpawn(cmd)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("cloning %s took longer than %s on this machine and was stopped — %s", strings.TrimSpace(repoURL), destCloneTimeout, gitFixHint(repoURL))
	}
	if err != nil {
		return fmt.Errorf("this machine can't reach %s — %s (git said: %s)", strings.TrimSpace(repoURL), gitFixHint(repoURL), gitReason(out, err))
	}
	return nil
}

// reportAssignment is the one call site for the assignment state endpoint. Best-effort: reporting
// is at-least-once server-side and a lost report must never abort work that already happened. A
// blank id (a bind that predates assignments) is a no-op.
func reportAssignment(d daemonDevice, assignmentID, state, reason string) {
	if strings.TrimSpace(assignmentID) == "" {
		return
	}
	_ = api.AssignmentState(d.Base, d.Token, assignmentID, state, reason)
}

// readvertiseAttempts / readvertiseWait: how hard we try to publish a just-registered project
// before giving up on it. Short and few — a heartbeat is already due within the minute, so this is
// about closing the assignment promptly, not about delivery.
const (
	readvertiseAttempts = 3
	readvertiseWait     = 3 * time.Second
)

// readvertise pushes a heartbeat carrying the newly registered project. Retried, because the whole
// point of gating 'ready' on it is that ready means "the control plane has seen this machine offer
// the project" — a single dropped packet must not turn a good clone into a failed assignment.
func readvertise(d daemonDevice) error {
	var err error
	for i := 0; i < readvertiseAttempts; i++ {
		if i > 0 {
			time.Sleep(readvertiseWait)
		}
		if err = api.Heartbeat(d.Base, d.Token, daemonConfigSnapshot()); err == nil {
			return nil
		}
	}
	return err
}

// handleAssignment is what the stream calls: the daemon-facing wiring (report over the API,
// re-advertise with a real heartbeat) around runAssignment, which holds the actual rule.
func handleAssignment(d daemonDevice, ev api.BindRepoEvent) {
	runAssignment(ev,
		func(state, reason string) { reportAssignment(d, ev.AssignmentID, state, reason) },
		func() error { return readvertise(d) })
}

// runAssignment guarantees a TERMINAL state is always reported. That guarantee is the point: an
// assignment left at 'cloning' is a row the web shows as in-progress forever and (per the dedupe
// rule) one that blocks re-assigning that project for half an hour. So every exit — success,
// refusal, or a panic anywhere underneath — ends in ready or failed.
func runAssignment(ev api.BindRepoEvent, send func(state, reason string), advertise func() error) {
	terminal := false
	report := func(state, reason string) {
		if state == "ready" || state == "failed" {
			terminal = true
		}
		send(state, reason)
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("\n⚠ project assignment %q hit an internal error: %v\n> ", ev.Label, r)
			report("failed", fmt.Sprintf("the daemon hit an internal error handling this assignment (%v) — nothing on this machine was overwritten", r))
			return
		}
		if !terminal { // belt and braces: no path may fall out of here still "cloning"
			report("failed", "the daemon stopped handling this assignment before it finished — nothing on this machine was overwritten")
		}
	}()

	if err := assignProject(ev, report, advertise); err != nil {
		fmt.Printf("\n⚠ project assignment %q failed: %v\n> ", ev.Label, err)
		report("failed", err.Error())
		return
	}
	fmt.Printf("\n📂 project %q is ready on this machine (from the web)\n> ", ev.Label)
}

// assignProject executes one destination assignment and REPORTS every transition it makes. The
// report function is injected so the state machine is exercisable without a control plane.
//
// Order matters and is the contract (#499): cloning → registering → ready, with 'ready' reported
// only after the project is in the local registry AND re-advertised on a heartbeat — otherwise the
// web would show a machine as ready to run a project it has not yet told anyone it has. A machine
// that already had the checkout skips straight past 'cloning' (forward skips are legal).
func assignProject(ev api.BindRepoEvent, report func(state, reason string), advertise func() error) error {
	label := strings.TrimSpace(ev.Label)
	if !labelRe.MatchString(label) {
		return fmt.Errorf("invalid project name %q", label)
	}
	parent := resolveDestinationHandle(ev.DestinationHandle)
	if parent == "" {
		return fmt.Errorf("this machine doesn't offer that destination directory (it may have been removed or the list is stale) — refresh the machine's destinations and pick one again")
	}
	repoURL, err := safeCloneURL(ev.RepoURL)
	if err != nil {
		return err
	}
	dir := filepath.Join(parent, label)
	needClone, err := inspectDestination(dir, repoURL)
	if err != nil {
		return err // refusal — nothing has been written
	}
	if needClone {
		_, statErr := os.Stat(dir)
		ourDir := os.IsNotExist(statErr) // we create the dir itself; otherwise it exists and is empty
		report("cloning", "")
		if err := os.MkdirAll(parent, 0o755); err != nil { // creates ~/partyline lazily, first assignment only
			return fmt.Errorf("can't create %s on this machine (%v)", parent, err)
		}
		if err := cloneRepo(dir, repoURL); err != nil {
			clearPartialClone(dir, ourDir)
			return err
		}
	}
	report("registering", "")
	if err := registerLocalProjectPolicy(label, dir, ev.Preset, ev.Engine, ev.Policy); err != nil {
		return err
	}
	// Re-advertise BEFORE calling it ready, and only call it ready if that SUCCEEDS:
	// registered-but-unadvertised is a machine the control plane would dispatch to and the picker
	// would not show, so reporting ready on a failed heartbeat would be a lie the web acts on.
	if err := advertise(); err != nil {
		return fmt.Errorf("%s is registered on this machine at %s, but it couldn't tell partyline (%v) — it will appear on the next heartbeat (within a minute); assign it again if it doesn't", label, dir, err)
	}
	report("ready", "")
	return nil
}
