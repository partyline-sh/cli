package main

// Release provenance: a pushed v* tag is the only gate in front of a Developer-ID-signed,
// notarized, cosign-signed release that every `ptln upgrade` installs. These tests hold the
// two properties that keep that gate honest: the tagged commit must already be on main (so a
// tag on an unreviewed branch cannot ship), and every action the release job runs is pinned
// to a commit, not a movable tag, because that job holds the signing and tap secrets.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func releaseGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// releaseGateStep returns the shell of release.yml's on-main step, exactly as the runner runs it.
func releaseGateStep(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "- name: Refuse tags that are not on main" {
			continue
		}
		if i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) != "run: |" {
			t.Fatal("the on-main step must be an inline run: | block, not a script from the tagged checkout")
		}
		indent := len(lines[i+1]) - len(strings.TrimLeft(lines[i+1], " ")) + 2
		var body []string
		for _, l := range lines[i+2:] {
			if strings.TrimSpace(l) != "" && len(l)-len(strings.TrimLeft(l, " ")) < indent {
				break
			}
			body = append(body, strings.TrimPrefix(l, strings.Repeat(" ", indent)))
		}
		return strings.Join(body, "\n")
	}
	t.Fatal("release.yml has no on-main step")
	return ""
}

func TestReleaseRefusesTagsOffMain(t *testing.T) {
	gate := releaseGateStep(t)
	origin := t.TempDir()
	releaseGit(t, origin, "init", "-q", "-b", "main")
	releaseGit(t, origin, "commit", "-q", "--allow-empty", "-m", "reviewed")
	onMain := releaseGit(t, origin, "rev-parse", "HEAD")
	releaseGit(t, origin, "checkout", "-q", "-b", "unreviewed")
	// The unreviewed commit tampers with anything in the checkout it can reach. The gate must not
	// depend on any of it.
	if err := os.MkdirAll(filepath.Join(origin, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "scripts", "release-on-main.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	releaseGit(t, origin, "add", "-A")
	releaseGit(t, origin, "commit", "-q", "-m", "never merged; neuters the gate script")
	offMain := releaseGit(t, origin, "rev-parse", "HEAD")
	releaseGit(t, origin, "checkout", "-q", "main")

	run := func(sha string) error {
		clone := filepath.Join(t.TempDir(), "clone")
		releaseGit(t, t.TempDir(), "clone", "-q", origin, clone)
		if sha != "" {
			releaseGit(t, clone, "checkout", "-q", sha) // the runner sits on the tagged commit
		}
		cmd := exec.Command("bash", "-c", gate)
		cmd.Dir = clone
		cmd.Env = append(os.Environ(), "GITHUB_SHA="+sha, "GITHUB_REF_NAME=v9.9.9", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%s", out)
		}
		return err
	}
	if err := run(onMain); err != nil {
		t.Fatalf("a commit on main must be releasable: %v", err)
	}
	if err := run(offMain); err == nil {
		t.Fatal("a tag on a commit that is not on main, carrying a neutered gate script, was allowed to release")
	}
	if err := run(""); err == nil {
		t.Fatal("an empty GITHUB_SHA must fail closed")
	}
}

func TestReleaseWorkflowIsPinnedAndGated(t *testing.T) {
	raw, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	wf := string(raw)
	uses := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)`).FindAllStringSubmatch(wf, -1)
	if len(uses) == 0 {
		t.Fatal("no actions found; the test is not reading the workflow")
	}
	pinned := regexp.MustCompile(`^[\w.-]+/[\w.-]+@[0-9a-f]{40}$`)
	for _, u := range uses {
		if !pinned.MatchString(u[1]) {
			t.Errorf("release action %q is not pinned to a full commit SHA", u[1])
		}
	}
	if regexp.MustCompile(`(?m)^\s*version:\s*"?~>`).MatchString(wf) {
		t.Error("goreleaser version is a range; pin it exactly")
	}
	gate := strings.Index(wf, "- name: Refuse tags that are not on main")
	test := strings.Index(wf, "go test -json")
	if gate < 0 {
		t.Fatal("release.yml has no on-main step")
	}
	if test >= 0 && gate > test {
		t.Error("the on-main gate must run before the tagged code's tests execute on the runner")
	}
}

func TestMirrorScriptKeepsTokenOutOfArgv(t *testing.T) {
	raw, err := os.ReadFile("scripts/mirror-cli.sh")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`https://[^"\s]*\$\{?TOKEN`).Match(raw) {
		t.Error("mirror-cli.sh puts the token in a URL, which is visible in ps on the persistent runner")
	}
}
