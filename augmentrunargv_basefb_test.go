package main

import (
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// A STACKED CHAIN member forks from its predecessor's branch. That branch is deleted the moment the
// predecessor's PR merges — the normal end state under merge policy `pr` — while run_tasks keeps the
// name, so the server hands over a ref that no longer exists on origin. crank then fails in under
// three seconds, before doing any work, and fails IDENTICALLY on every retry: the predecessor stays
// done, the name stays in the DB, the branch stays deleted. A run in that state can never finish.
//
// base_fallback carries the project base so crank can fork from it instead. That is not a guess —
// once the predecessor has merged, its commits ARE in the project base.
const fbRunID = "12345678-1234-1234-1234-123456789abc"

func argvFor(ev api.RunEvent) []string {
	ev.RunID = fbRunID
	argv, err := augmentRunArgv([]string{"crank"}, ev)
	if err != nil {
		return nil
	}
	return argv
}

func hasPair(argv []string, flag, val string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}

func TestStackedMemberCarriesItsFallback(t *testing.T) {
	argv := argvFor(api.RunEvent{BaseBranch: "crank-98a630f2-01-Verify", BaseFallback: "staging"})
	if !hasPair(argv, "--base", "crank-98a630f2-01-Verify") {
		t.Fatalf("stacked base missing: %v", argv)
	}
	if !hasPair(argv, "--base-fallback", "staging") {
		t.Fatalf("fallback missing — the member cannot recover once its predecessor merges: %v", argv)
	}
}

// An operator-configured base that is missing from origin must stay a LOUD failure: a branch built on
// the wrong base becomes a PR against the wrong target, which is worse than stopping. Only a stacked
// member — whose base is derived, not configured — gets to fall back.
func TestUnstackedRunGetsNoFallback(t *testing.T) {
	argv := argvFor(api.RunEvent{BaseBranch: "staging"})
	if strings.Contains(strings.Join(argv, " "), "--base-fallback") {
		t.Fatalf("a non-stacked run must not be able to silently retarget: %v", argv)
	}
}

// Guard the degenerate case: fallback == base carries no information and would only add noise.
func TestFallbackEqualToBaseIsNotSent(t *testing.T) {
	argv := argvFor(api.RunEvent{BaseBranch: "staging", BaseFallback: "staging"})
	if strings.Contains(strings.Join(argv, " "), "--base-fallback") {
		t.Fatalf("identical fallback should be omitted: %v", argv)
	}
}

// Same shape gate as --base. A server value must never reach argv unvalidated — a fallback carrying a
// flag or a path would be an injection point on every machine in the fleet.
func TestMalformedFallbackIsRejected(t *testing.T) {
	for _, bad := range []string{"--dangerously-skip-permissions", "../../etc/passwd", "a b", "-x", "--model"} {
		argv := argvFor(api.RunEvent{BaseBranch: "crank-x-01", BaseFallback: bad})
		// Assert on the FLAG PAIR, not a substring: "-x" is a substring of the legitimate base
		// "crank-x-01", so a contains-check reports a failure that never happened.
		if hasPair(argv, "--base-fallback", bad) {
			t.Fatalf("malformed fallback %q reached argv: %v", bad, argv)
		}
		for _, a := range argv {
			if a == "--base-fallback" {
				t.Fatalf("a rejected fallback still emitted the flag (dangling arg): %v", argv)
			}
		}
	}
}
