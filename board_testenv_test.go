package main

import "testing"

// isolateInstance points HOME at a scratch dir for tests that assert what happens with NO control
// plane configured.
//
// Base() resolves PARTYLINE_API → the machine's remembered instance (~/.partyline/instance) →
// the historical default. A test that sets PARTYLINE_API="" and expects production is therefore
// only correct on a machine that has never signed in to a self-hosted instance — it passed in CI,
// which has no such file, and failed on any developer box that had one. Same shape as the
// install-dir leak that made discovery tests pass on one runner and fail on another.
//
// Tests about "unconfigured" behaviour must actually be unconfigured.
func isolateInstance(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}
