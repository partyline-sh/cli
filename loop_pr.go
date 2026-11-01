package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// `ptln loop pr <id> <url-or-number>`: the session records the PR it opened once the run reached
// pr_ready, so the Loops board can show the PR and its CI state on the Done card.

// loopPRURL is a GitHub pull request URL; the number is its last path element.
var loopPRURL = regexp.MustCompile(`^https?://[^/\s]+/[^/\s]+/[^/\s]+/pull/([0-9]+)/?$`)

// loopPRCmd is `ptln loop pr`.
func loopPRCmd(args []string) {
	if len(args) != 2 {
		fatal(fmt.Errorf("usage: ptln loop pr <id> <url-or-number>"))
	}
	n, err := recordLoopPR(args[0], args[1])
	if err != nil {
		fatal(err)
	}
	fmt.Printf("loop %s: recorded PR #%d\n", args[0], n)
}

// recordLoopPR stores the PR on the run and logs a "pr" event; it returns the PR's number.
func recordLoopPR(id, ref string) (int, error) {
	pr, err := parseLoopPR(ref)
	if err != nil {
		return 0, err
	}
	r, err := loadLoopRun(id)
	if err != nil {
		return 0, err
	}
	if r.State != loopPRReady {
		return 0, fmt.Errorf("loop %s is %s; a PR is recorded once the run reaches pr_ready", r.ID, r.State)
	}
	r.PR = pr
	if err := r.save(loopEvent{Type: "pr", Data: map[string]any{"pr": pr}}); err != nil {
		return 0, err
	}
	return loopPRNumber(pr), nil
}

// parseLoopPR accepts "123", "#123" or a pull request URL, and returns the number or the URL.
func parseLoopPR(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.Atoi(strings.TrimPrefix(ref, "#")); err == nil && n > 0 {
		return strconv.Itoa(n), nil
	}
	if loopPRURL.MatchString(ref) {
		return strings.TrimSuffix(ref, "/"), nil
	}
	return "", fmt.Errorf("%q is not a PR number or a pull request URL", ref)
}

// loopPRNumber is the number of a recorded PR, 0 when there is none.
func loopPRNumber(pr string) int {
	n, _ := strconv.Atoi(pr[strings.LastIndex(pr, "/")+1:])
	return n
}
