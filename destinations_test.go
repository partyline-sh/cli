package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// These tests exist for the two failures that would actually hurt: a machine cloning somewhere it
// never offered (or on top of someone's work), and a fresh box showing an empty destination picker.
// Everything here runs against a temp tree with injected roots — never the machine's real home.

// failIfAdvertised is the advertise step for assignments that must never reach it.
func failIfAdvertised(t *testing.T) func() error {
	t.Helper()
	return func() error {
		t.Fatal("a rejected assignment must not re-advertise")
		return nil
	}
}

// A brand-new machine — no repos, no scan roots — must still offer somewhere to put code.
func TestDefaultWorkspaceIsAlwaysAdvertised(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "partyline") // deliberately NOT created: it is made lazily

	paths := destinationPaths([]string{home}, home, ws, nil)
	if len(paths) != 1 || paths[0] != ws {
		t.Fatalf("fresh machine should offer exactly the default workspace, got %v", paths)
	}
	dests := destinationsFrom(paths, home, ws, []string{home})
	if len(dests) != 1 || dests[0].Label != "default workspace" {
		t.Fatalf("expected a labelled fallback candidate, got %+v", dests)
	}
	if dests[0].Handle != localRepoHandle(ws) {
		t.Fatalf("handle must be the hash of the local path")
	}
	if strings.Contains(dests[0].Parent, home) {
		t.Fatalf("display parent %q leaks the absolute path", dests[0].Parent)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatalf("advertising must not create the workspace directory")
	}
}

// The real list: where repos already live, plus registered scan roots, never $HOME itself.
func TestDestinationCandidates(t *testing.T) {
	home := t.TempDir()
	mount := t.TempDir()
	dev := filepath.Join(home, "dev")
	mkRepo(t, filepath.Join(dev, "app"))
	mkRepo(t, filepath.Join(dev, "web"))
	mkRepo(t, filepath.Join(mount, "code", "svc"))
	ws := filepath.Join(home, "partyline")

	roots := []string{home, mount}
	repos := []string{filepath.Join(dev, "app"), filepath.Join(dev, "web"), filepath.Join(mount, "code", "svc")}
	paths := destinationPaths(roots, home, ws, repos)

	want := map[string]bool{ws: true, mount: true, dev: true, filepath.Join(mount, "code"): true}
	if len(paths) != len(want) {
		t.Fatalf("got %v, want exactly %v", paths, want)
	}
	for _, p := range paths {
		if !want[p] {
			t.Fatalf("unexpected candidate %q", p)
		}
		if p == home {
			t.Fatalf("$HOME must never be offered as a destination")
		}
	}
	// Handles are unique, and the parents rendered for display carry no absolute path.
	seen := map[string]bool{}
	for _, d := range destinationsFrom(paths, home, ws, roots) {
		if seen[d.Handle] {
			t.Fatalf("duplicate handle %q", d.Handle)
		}
		seen[d.Handle] = true
		if strings.HasPrefix(d.Parent, "/") {
			t.Fatalf("display parent %q is an absolute path", d.Parent)
		}
	}
}

// A handle this machine never advertised resolves to nothing — the whole reference-not-command wall.
func TestUnadvertisedHandleIsRejected(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(home, "partyline")
	paths := destinationPaths([]string{home}, home, ws, nil)

	for _, bogus := range []string{
		"",
		"deadbeefdeadbeef",
		localRepoHandle("/etc"),                 // a real directory we simply never offered
		localRepoHandle(filepath.Join(ws, "x")), // a child of one we did
	} {
		if got := resolveDestinationHandleIn(paths, bogus); got != "" {
			t.Fatalf("handle %q resolved to %q — it was never advertised", bogus, got)
		}
	}
	if got := resolveDestinationHandleIn(paths, localRepoHandle(ws)); got != ws {
		t.Fatalf("advertised handle should resolve to %q, got %q", ws, got)
	}
}

// Only remote URLs may reach `git clone`, and never as a flag or a local/ext transport.
func TestSafeCloneURL(t *testing.T) {
	for _, ok := range []string{
		"https://github.com/you/x.git",
		"ssh://git@github.com/you/x.git",
		"git@github.com:you/x.git",
	} {
		if _, err := safeCloneURL(ok); err != nil {
			t.Fatalf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "   ",
		"--upload-pack=touch /tmp/pwn",
		"-u https://github.com/you/x",
		"ext::sh -c 'touch /tmp/pwn'",
		"file:///etc",
		"/etc/passwd",
		"../../evil",
		"https://github.com/you/x; rm -rf /",
	} {
		if _, err := safeCloneURL(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestRemoteKeyNormalizes(t *testing.T) {
	same := []string{
		"https://github.com/You/X.git",
		"git@github.com:you/x.git",
		"ssh://git@github.com/you/x",
		"https://github.com/you/x/",
	}
	want := remoteKey(same[0])
	for _, u := range same[1:] {
		if got := remoteKey(u); got != want {
			t.Fatalf("%q → %q, want %q", u, got, want)
		}
	}
	if remoteKey("https://github.com/you/other") == want {
		t.Fatalf("different repos must not compare equal")
	}
}

// git-backed helper: a real checkout with a real origin, so inspectDestination is exercised against
// git itself rather than a mock of it.
func mkCheckout(t *testing.T, dir, origin string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", origin},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v — %s", args, err, out)
		}
	}
}

// The refusals: a destination that is occupied by anything other than the exact repo asked for is
// never touched, and the reason names the directory.
func TestInspectDestinationRefusesWithoutWriting(t *testing.T) {
	parent := t.TempDir()
	repoURL := "https://github.com/you/x.git"

	t.Run("missing → clone", func(t *testing.T) {
		need, err := inspectDestination(filepath.Join(parent, "fresh"), repoURL)
		if err != nil || !need {
			t.Fatalf("need=%v err=%v", need, err)
		}
		if _, err := os.Stat(filepath.Join(parent, "fresh")); !os.IsNotExist(err) {
			t.Fatalf("inspection must not create anything")
		}
	})

	t.Run("already the right repo → register, no clone", func(t *testing.T) {
		dir := filepath.Join(parent, "have")
		mkCheckout(t, dir, "git@github.com:you/x.git") // the other spelling of the same repo
		need, err := inspectDestination(dir, repoURL)
		if err != nil || need {
			t.Fatalf("need=%v err=%v — an existing checkout must be registered, not re-cloned", need, err)
		}
	})

	t.Run("a different repo → refuse", func(t *testing.T) {
		dir := filepath.Join(parent, "other")
		mkCheckout(t, dir, "https://github.com/someone/else.git")
		need, err := inspectDestination(dir, repoURL)
		if err == nil || need {
			t.Fatalf("expected a refusal, got need=%v err=%v", need, err)
		}
		if !strings.Contains(err.Error(), dir) {
			t.Fatalf("refusal must name the directory: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr != nil {
			t.Fatalf("the existing checkout must be left alone")
		}
	})

	t.Run("non-empty, not a repo → refuse", func(t *testing.T) {
		dir := filepath.Join(parent, "busy")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		need, err := inspectDestination(dir, repoURL)
		if err == nil || need {
			t.Fatalf("expected a refusal, got need=%v err=%v", need, err)
		}
		if !strings.Contains(err.Error(), dir) {
			t.Fatalf("refusal must name the directory: %v", err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Fatalf("the directory's contents must be untouched")
		}
	})

	t.Run("empty directory → clone into it", func(t *testing.T) {
		dir := filepath.Join(parent, "empty")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		need, err := inspectDestination(dir, repoURL)
		if err != nil || !need {
			t.Fatalf("need=%v err=%v", need, err)
		}
	})
}

// The failure reason a human reads has to name the remote AND the fix on THIS machine.
func TestGitFixHintNamesTheFix(t *testing.T) {
	if got := gitFixHint("git@github.com:you/x.git"); !strings.Contains(got, "gh auth login") {
		t.Fatalf("github hint should point at gh auth login, got %q", got)
	}
	if got := gitFixHint("https://git.corp.internal/team/x.git"); !strings.Contains(got, "git.corp.internal") {
		t.Fatalf("hint should name the host, got %q", got)
	}
}

// An assignment naming a handle we never advertised fails with a legible reason and writes nothing.
func TestAssignProjectRejectsUnknownHandle(t *testing.T) {
	dir := realPath(t.TempDir()) // macOS /var→/private/var: a handle is the hash of the EXACT string the scan reports
	t.Setenv("HOME", dir)        // localRepoRoots/defaultWorkspacePath read the real home otherwise
	var states []string
	err := assignProject(api.BindRepoEvent{
		Label:             "demo",
		DestinationHandle: "0000000000000000",
		RepoURL:           "https://github.com/you/x.git",
	}, func(state, _ string) { states = append(states, state) }, failIfAdvertised(t))
	if err == nil {
		t.Fatal("an unadvertised destination handle must be rejected")
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Fatalf("reason should say the destination is unknown: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("a rejected assignment must report no progress states, got %v", states)
	}
	// Nothing was created for the assignment: no default workspace, no <parent>/<label>.
	// (~/.partyline is the daemon's own state dir, read to derive the candidate list.)
	for _, must := range []string{filepath.Join(dir, "partyline"), filepath.Join(dir, "demo")} {
		if _, statErr := os.Stat(must); !os.IsNotExist(statErr) {
			t.Fatalf("a rejected assignment must not create %s", must)
		}
	}
}

// The happy path that must NOT clone: the machine already has the repo at <parent>/<label>, so the
// assignment registers it and reports registering → ready, in that order, with no 'cloning'.
func TestAssignProjectRegistersExistingCheckout(t *testing.T) {
	home := realPath(t.TempDir()) // macOS /var→/private/var: a handle is the hash of the EXACT string the scan reports
	t.Setenv("HOME", home)
	dev := filepath.Join(home, "dev")
	mkCheckout(t, filepath.Join(dev, "demo"), "git@github.com:you/demo.git")

	var states []string
	advertised := 0
	err := assignProject(api.BindRepoEvent{
		Label:             "demo",
		DestinationHandle: localRepoHandle(dev), // ~/dev is advertised: it already holds a repo
		RepoURL:           "https://github.com/you/demo.git",
	}, func(state, _ string) { states = append(states, state) }, func() error { advertised++; return nil })
	if err != nil {
		t.Fatalf("assignment should have succeeded: %v", err)
	}
	if strings.Join(states, ",") != "registering,ready" {
		t.Fatalf("states = %v, want registering then ready (no clone)", states)
	}
	if advertised != 1 {
		t.Fatalf("ready must be preceded by exactly one re-advertise, got %d", advertised)
	}
	got := projectByLabel(loadDaemonRegistry(), "demo")
	if got == nil || got.Path != filepath.Join(dev, "demo") {
		t.Fatalf("project not registered at the existing checkout: %+v", got)
	}
}

// cloneInto's plumbing (env, process group, output→reason) against a real local remote. Not a
// network test: file:// is a transport git treats like any other, and safeCloneURL refuses it on
// the assignment path — this exercises the runner, not the allow-list.
func TestCloneIntoRunsGitAndReportsFailure(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	mkCheckout(t, src, "https://github.com/you/x.git")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", src}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v — %s", args, err, out)
		}
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := cloneInto(dst, "file://"+src); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if !isGitRepo(dst) {
		t.Fatalf("%s is not a checkout", dst)
	}

	// A remote that cannot be reached: the reason names the remote AND the fix on this machine.
	err := cloneInto(filepath.Join(t.TempDir(), "nope"), "file:///nonexistent/repo.git")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "/nonexistent/repo.git") || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("reason should name the remote and the fix: %v", err)
	}
}

// stubClone replaces the clone step for the duration of a test.
func stubClone(t *testing.T, fn func(dir, repoURL string) error) {
	t.Helper()
	prev := cloneRepo
	cloneRepo = fn
	t.Cleanup(func() { cloneRepo = prev })
}

// writePartialCheckout is what a KILLED clone leaves behind: a .git with the right origin in it,
// and no working tree. It is indistinguishable from a finished clone to inspectDestination, which
// is precisely why a failed clone must not leave one lying around.
func writePartialCheckout(t *testing.T, dir, origin string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = " + origin + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// THE REGRESSION: a clone that dies mid-write leaves a partial checkout whose origin matches, so
// the next attempt would register an incomplete tree as ready. The partial tree must be gone.
func TestFailedCloneLeavesNothingBehind(t *testing.T) {
	home := realPath(t.TempDir())
	t.Setenv("HOME", home)
	ws := filepath.Join(home, "partyline") // the always-advertised fallback destination
	repoURL := "https://github.com/you/demo.git"

	stubClone(t, func(dir, url string) error {
		writePartialCheckout(t, dir, url) // git got far enough to write .git, then was killed
		return fmt.Errorf("killed")
	})

	var states []string
	err := assignProject(api.BindRepoEvent{
		Label:             "demo",
		DestinationHandle: localRepoHandle(ws),
		RepoURL:           repoURL,
	}, func(state, _ string) { states = append(states, state) }, failIfAdvertised(t))
	if err == nil {
		t.Fatal("a failed clone must fail the assignment")
	}
	if strings.Join(states, ",") != "cloning" {
		t.Fatalf("states = %v, want cloning only (never registering/ready)", states)
	}
	if _, statErr := os.Stat(filepath.Join(ws, "demo")); !os.IsNotExist(statErr) {
		t.Fatalf("the partial checkout must be removed, not left for the next attempt to adopt")
	}
	// And the retry therefore starts clean rather than registering the wreckage.
	need, inspectErr := inspectDestination(filepath.Join(ws, "demo"), repoURL)
	if inspectErr != nil || !need {
		t.Fatalf("a retry should see an empty destination, got need=%v err=%v", need, inspectErr)
	}
}

// The same cleanup, when the destination was an EMPTY directory we did not create: its contents go,
// the directory itself stays (it was never ours).
func TestFailedCloneClearsAnEmptyDirWeFilled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writePartialCheckout(t, dir, "https://github.com/you/demo.git")

	clearPartialClone(dir, false)

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("a directory we did not create must survive: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the partial checkout we wrote must be cleared, found %v", entries)
	}
}

// 'ready' means "the control plane has seen this machine offer the project". If the re-advertise
// fails, ready is a lie — the assignment fails instead, and says the project IS registered.
func TestReadyRequiresTheReadvertiseToSucceed(t *testing.T) {
	home := realPath(t.TempDir())
	t.Setenv("HOME", home)
	dev := filepath.Join(home, "dev")
	mkCheckout(t, filepath.Join(dev, "demo"), "https://github.com/you/demo.git")

	var states []string
	err := assignProject(api.BindRepoEvent{
		Label:             "demo",
		DestinationHandle: localRepoHandle(dev),
		RepoURL:           "https://github.com/you/demo.git",
	}, func(state, _ string) { states = append(states, state) },
		func() error { return fmt.Errorf("no route to host") })

	if err == nil {
		t.Fatal("a failed re-advertise must not produce a ready assignment")
	}
	if has(states, "ready") {
		t.Fatalf("ready reported without a successful heartbeat: %v", states)
	}
	if !strings.Contains(err.Error(), "registered") || !strings.Contains(err.Error(), "no route to host") {
		t.Fatalf("the reason must say it IS registered and why it couldn't be published: %v", err)
	}
}

// No path may leave an assignment sitting at 'cloning' — including a panic underneath it.
func TestAssignmentAlwaysEndsInATerminalState(t *testing.T) {
	home := realPath(t.TempDir())
	t.Setenv("HOME", home)
	dev := filepath.Join(home, "dev")
	mkCheckout(t, filepath.Join(dev, "demo"), "https://github.com/you/demo.git")

	var states []string
	runAssignment(api.BindRepoEvent{
		Label:             "demo",
		DestinationHandle: localRepoHandle(dev),
		RepoURL:           "https://github.com/you/demo.git",
	}, func(state, _ string) { states = append(states, state) },
		func() error { panic("boom") })

	if len(states) == 0 || states[len(states)-1] != "failed" {
		t.Fatalf("states = %v, want a terminal 'failed' after a panic", states)
	}
}

// The heartbeat advertises two lists derived from the SAME filesystem walk. Deriving them from
// separate scans walked the home directory twice per beat — on the machine whose home walk once
// took ten minutes, that is not a rounding error.
func TestHeartbeatListsShareOneWalk(t *testing.T) {
	home := realPath(t.TempDir())
	t.Setenv("HOME", home)
	mkRepo(t, filepath.Join(home, "dev", "app"))
	invalidateLocalRepoCache()
	t.Cleanup(invalidateLocalRepoCache)

	repos := cachedLocalRepos()
	repoScanCache.mu.Lock()
	firstWalk := repoScanCache.at
	repoScanCache.mu.Unlock()

	dests := cachedDestinations()
	repoScanCache.mu.Lock()
	secondWalk := repoScanCache.at
	repoScanCache.mu.Unlock()

	if firstWalk.IsZero() || !secondWalk.Equal(firstWalk) {
		t.Fatalf("the second list re-walked the disk (%v → %v)", firstWalk, secondWalk)
	}
	if len(repos) != 1 || len(dests) == 0 {
		t.Fatalf("both views must still be populated: repos=%v dests=%v", repos, dests)
	}
}
