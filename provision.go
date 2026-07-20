package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"partyline.sh/partyline/internal/api"
)

// provision.go — PROVISIONED WORKERS, P2 (docs/plans/provisioned-workers.md). Clone-on-demand
// execution for a run whose LABEL the daemon has NOT locally registered: the control plane names a
// REPO in its own trust domain (owner/name, resolved from the org's project record), and THIS daemon
// derives every filesystem path itself, mints a short-lived repo-scoped token, clones into a managed
// dir it owns, and hands the SAME fixed crank argv the registry path would. resolveRun stays the
// untouched registry chokepoint; provisionRun is a SECOND, opt-in path with its own validation.
//
// SECURITY POSTURE (how the reference-not-command boundary holds when the source of truth moves to
// the control plane):
//   - Opt-in per node: nothing provisions unless the operator ran `ptln daemon provision on`.
//   - The repo full_name is re-validated (repoFullNameRe) BEFORE it becomes a path — the single wall
//     against a server string escaping into the filesystem. The daemon builds the dir; the server
//     never supplies a path.
//   - The clone credential is a short-lived (~1h), repo-scoped GitHub App token fetched at clone time
//     from the existing github-token endpoint. It never touches argv (would show in `ps`), the remote
//     URL, or .git/config — it rides an env var read by a per-invocation credential helper.
//   - The crank argv is built by the SAME buildRunArgv the registry path uses (worklist as a DATA
//     file, fixed flags) — no ref field becomes a flag.

const provisionReposSub = "repos" // managed clones live at <state>/daemon/repos/<owner>/<name>

// repoFullNameRe mirrors the web REPO_FULL_NAME_RE: "owner/name", anchored, exactly one slash, no
// traversal / whitespace / shell metacharacters. The ONE gate before a server-supplied repo name is
// used to derive a path.
var repoFullNameRe = regexp.MustCompile(`^[A-Za-z0-9][\w.-]*/[A-Za-z0-9][\w.-]*$`)

// provisionStatePath is the per-node opt-in marker. Its existence = provisioning enabled. Kept OUT of
// the registry (registry = owner-authored project bindings; this is a capability toggle) and out of
// device.json (that's identity). A plain marker file the operator flips with `ptln daemon provision`.
func provisionStatePath() string { return filepath.Join(stateDir(), "daemon", "provision.on") }

// provisionEnabled reports whether this node opted into clone-on-demand work. Read at dispatch (the
// hard gate before any clone) and in the heartbeat snapshot (so the control plane's picker + enqueue
// gate only offer provisioned dispatch to nodes that turned it on).
func provisionEnabled() bool {
	_, err := os.Stat(provisionStatePath())
	return err == nil
}

func setProvisionEnabled(on bool) error {
	p := provisionStatePath()
	if on {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		return os.WriteFile(p, []byte("on\n"), 0o600)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// managedRepoDir derives the daemon-owned clone path for a repo full_name, re-validating the name
// first. "" on an invalid name — the caller refuses the run. Keyed by repo (not label), so two
// projects pointing at the same repo share one persistent clone.
func managedRepoDir(fullName string) string {
	full := strings.TrimSpace(fullName)
	if !repoFullNameRe.MatchString(full) {
		return ""
	}
	parts := strings.SplitN(full, "/", 2) // exactly one slash by the regex
	return filepath.Join(stateDir(), "daemon", provisionReposSub, parts[0], parts[1])
}

func isGitRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && fi.IsDir()
}

// gitCredHelper echoes the token as the password for x-access-token. The token itself is read from
// $PTLN_GIT_TOKEN in the env — never inlined here or in argv.
const gitCredHelper = `!f() { echo username=x-access-token; echo "password=$PTLN_GIT_TOKEN"; }; f`

// gitCredArgs clears any inherited credential helper then installs ours for THIS invocation only, so
// nothing is written to .git/config.
func gitCredArgs() []string {
	return []string{"-c", "credential.helper=", "-c", "credential.helper=" + gitCredHelper}
}

// cloneOrFetch materializes the managed clone: a fresh `git clone` the first time, a `git fetch`
// (prune) on reuse so a new run branches off up-to-date origin state. The token rides the env for the
// credential helper only. Errors carry git's output so a failure is legible on the board.
func cloneOrFetch(dir, cloneURL, token string) error {
	env := append(os.Environ(), "PTLN_GIT_TOKEN="+token, "GIT_TERMINAL_PROMPT=0")
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			// git echoes the token nowhere (it's in the helper env), so the output is safe to surface.
			return fmt.Errorf("git %s: %v — %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if isGitRepo(dir) {
		args := append([]string{"-C", dir}, gitCredArgs()...)
		return run(append(args, "fetch", "origin", "--prune")...)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	args := append([]string{}, gitCredArgs()...)
	return run(append(args, "clone", cloneURL, dir)...)
}

// engineBinName maps a run engine to the binary a preflight can check for. "" for unknown/empty
// (claude default is still checked) — crank remains the authoritative engine check.
func engineBinName(engine string) string {
	switch strings.TrimSpace(strings.ToLower(engine)) {
	case "", "claude":
		return "claude"
	case "codex":
		return "codex"
	case "gemini":
		return "gemini"
	case "antigravity":
		return "agy"
	default:
		return ""
	}
}

// preflightProvisionedRun fails CHEAP and LEGIBLE before crank burns any tokens: the run's engine
// binary must be on PATH, and every tool named in the freshly-cloned repo's .partyline/verify must
// resolve — otherwise a build would run for minutes and then die at verify with a cryptic error.
// Returns a plain-language, tool-named error, or nil when the node can actually run this repo.
func preflightProvisionedRun(dir, engine string) error {
	if bin := engineBinName(engine); bin != "" {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("this node can't run the %q engine — %q isn't installed (or not on PATH)", engineLabelOr(engine), bin)
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, tool := range verifyCommandTools(dir) {
		if seen[tool] {
			continue
		}
		seen[tool] = true
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("this node is missing the tool(s) this project's verify step needs: %s — install them or run this project on a machine that has them", strings.Join(missing, ", "))
	}
	return nil
}

func engineLabelOr(engine string) string {
	if e := strings.TrimSpace(engine); e != "" {
		return e
	}
	return "claude"
}

// verifyCommandTools reads the repo's .partyline/verify (one shell command per line) and returns the
// FIRST token of each — the binary the line would invoke. Comments/blank lines skipped. Best-effort:
// no file → no tools to check. This is a read of committed repo content (safe), used only to LookPath.
func verifyCommandTools(dir string) []string {
	f, err := os.Open(filepath.Join(dir, ".partyline", "verify"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var tools []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			// env-style "FOO=bar cmd" — skip leading VAR=VALUE assignments to find the real command.
			for _, tok := range fields {
				if strings.Contains(tok, "=") && !strings.ContainsAny(tok, "/.") {
					continue
				}
				tools = append(tools, tok)
				break
			}
		}
	}
	return tools
}

// provisionRun is the clone-on-demand twin of resolveRun: produce (argv, dir) for a provisioned run
// by fetching the repo manifest, minting a clone token, cloning/fetching into a managed dir, running
// preflight, and building the SAME crank argv. Any failure returns an error (no argv) → spawnRun marks
// the run failed with the reason (legible on the board), no crank spawned.
func provisionRun(d daemonDevice, ev api.RunEvent) (argv []string, dir string, err error) {
	m, err := api.RunProvisionManifest(d.Base, d.Token, ev.RunID)
	if err != nil {
		return nil, "", fmt.Errorf("provision manifest: %w", err)
	}
	dir = managedRepoDir(m.Repo.FullName)
	if dir == "" {
		return nil, "", fmt.Errorf("provision: control plane returned an invalid repo %q", m.Repo.FullName)
	}
	cloneURL := strings.TrimSpace(m.Repo.CloneURL)
	if !strings.HasPrefix(cloneURL, "https://github.com/") {
		return nil, "", fmt.Errorf("provision: unexpected clone url %q", cloneURL)
	}
	token, err := api.RunGitHubToken(d.Base, d.Token, ev.RunID)
	if err != nil || strings.TrimSpace(token) == "" {
		return nil, "", fmt.Errorf("provision: no clone credential — connect the GitHub App for this workspace (%v)", err)
	}
	fmt.Fprintf(os.Stderr, "⇣ provisioning %s → %s\n", m.Repo.FullName, dir)
	if err := cloneOrFetch(dir, cloneURL, token); err != nil {
		return nil, "", fmt.Errorf("provision clone: %w", err)
	}
	if err := preflightProvisionedRun(dir, ev.Engine); err != nil {
		return nil, "", err // already a plain-language, tool-named message
	}
	argv, err = buildRunArgv(runRefFromEvent(ev))
	if err != nil {
		return nil, "", err
	}
	return argv, dir, nil
}

// daemonProvision — `ptln daemon provision [on|off|status]`. The per-node consent gate for being a
// clone-on-demand worker: enroll this machine to run projects it doesn't have checked out (the
// control plane's job queue can then clone repos onto it). Off by default.
func daemonProvision(args []string) {
	action := "status"
	if len(args) > 0 {
		action = strings.TrimSpace(strings.ToLower(args[0]))
	}
	switch action {
	case "on", "enable":
		if err := setProvisionEnabled(true); err != nil {
			fatal(fmt.Errorf("enable provision: %w", err))
		}
		fmt.Println("✓ provision mode ON — this machine can run projects it doesn't have locally (it clones them on demand).")
		fmt.Println("  It will only ever clone repos your workspace explicitly configured, using short-lived tokens. Turn off with: ptln daemon provision off")
	case "off", "disable":
		if err := setProvisionEnabled(false); err != nil {
			fatal(fmt.Errorf("disable provision: %w", err))
		}
		fmt.Println("✓ provision mode OFF — this machine only runs projects in its local registry.")
	case "status":
		if provisionEnabled() {
			fmt.Println("provision mode: ON (this machine accepts clone-on-demand work)")
		} else {
			fmt.Println("provision mode: OFF (registry projects only) — enable with: ptln daemon provision on")
		}
	default:
		fatal(fmt.Errorf("usage: ptln daemon provision [on|off|status]"))
	}
}
