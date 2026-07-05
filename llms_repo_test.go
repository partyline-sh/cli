package main

import "testing"

func TestStripURLCreds(t *testing.T) {
	cases := []struct{ in, want string }{
		{"git@github.com:darcyreno/partyline.git", "git@github.com:darcyreno/partyline.git"},         // ssh: untouched
		{"https://github.com/darcyreno/partyline.git", "https://github.com/darcyreno/partyline.git"}, // clean https: untouched
		{"https://x-access-token:ghp_SECRET@github.com/o/r.git", "https://github.com/o/r.git"},       // token stripped
		{"https://user:pass@gitlab.com/o/r.git", "https://gitlab.com/o/r.git"},                       // basic-auth stripped
		{"ssh://git@host:22/o/r.git", "ssh://host:22/o/r.git"},                                       // ssh:// authority userinfo stripped (no secret, but consistent)
		{"", ""},
		{"/local/path", "/local/path"}, // no scheme
	}
	for _, c := range cases {
		if got := stripURLCreds(c.in); got != c.want {
			t.Errorf("stripURLCreds(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
