package sshd

import "testing"

func TestOptionsAllowed(t *testing.T) {
	// empty allowlist → everyone is allowed (authz is off, only authn applies)
	if !(Options{}).allowed("anyone") {
		t.Error("empty Allow should permit any user")
	}
	o := Options{Allow: []string{"Alice", " bob ", "carol-dev"}}
	cases := map[string]bool{
		"alice":     true,  // case-insensitive
		"BOB":       true,  // trimmed + case-insensitive
		"carol-dev": true,  // exact
		"carol":     false, // not a prefix match
		"dave":      false, // not listed
		"":          false,
	}
	for user, want := range cases {
		if got := o.allowed(user); got != want {
			t.Errorf("allowed(%q) = %v, want %v", user, got, want)
		}
	}
}

func TestGitHubUserRE(t *testing.T) {
	valid := []string{"a", "alice", "octo-cat", "a1-b2-c3", "ABC123",
		"abcdefghijklmnopqrstuvwxyz0123456789abc"} // 39 chars (max)
	for _, u := range valid {
		if !ghUserRE.MatchString(u) {
			t.Errorf("expected valid github username: %q", u)
		}
	}
	invalid := []string{
		"",            // empty
		"-bad",        // leading hyphen
		"bad-",        // trailing hyphen
		"has/slash",   // path traversal into the .keys URL
		"has.dot",     // not allowed
		"has space",   // space
		"under_score", // underscore
		"abcdefghijklmnopqrstuvwxyz0123456789abcd", // 40 chars (over max)
	}
	for _, u := range invalid {
		if ghUserRE.MatchString(u) {
			t.Errorf("expected INVALID github username (anti-SSRF): %q", u)
		}
	}
}
