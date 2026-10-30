package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The single most important property: production paths are UNCHANGED. Every existing install keeps
// ~/.partyline/token and ~/.partyline/account.json, with no migration and nothing to notice. If
// this test ever fails, an upgrade silently logs everyone out.
func TestProdPathsUnchanged(t *testing.T) {
	// Genuinely unconfigured — see withHome. Without this the test reads the developer's own
	// ~/.partyline/instance and "production" becomes whatever box they last signed in to.
	withHome(t)
	home, _ := os.UserHomeDir()
	if got, want := ConfigDir(), filepath.Join(home, ".partyline"); got != want {
		t.Fatalf("prod config dir moved: got %s, want %s", got, want)
	}
	if !IsProd() {
		t.Fatal("empty PARTYLINE_API must mean production")
	}
	if EnvLabel() != "" {
		t.Fatalf("production must have no env label, got %q", EnvLabel())
	}

	// An explicit prod URL is the same thing, trailing slash or not.
	for _, v := range []string{"https://partyline.sh", "https://partyline.sh/"} {
		t.Setenv("PARTYLINE_API", v)
		if !IsProd() {
			t.Fatalf("%s should be production", v)
		}
		if got, want := ConfigDir(), filepath.Join(home, ".partyline"); got != want {
			t.Fatalf("%s: got %s, want %s", v, got, want)
		}
	}
}

func TestNonProdIsIsolated(t *testing.T) {
	home, _ := os.UserHomeDir()
	prod := filepath.Join(home, ".partyline")

	// The colon in a host:port is sanitised out — it is a path segment here, and a literal colon
	// is awkward-to-illegal in a path depending on the filesystem.
	cases := map[string]string{
		"https://staging.partyline.sh": "staging.partyline.sh",
		"http://localhost:3111":        "localhost_3111",
		"https://ptln.example.com":     "ptln.example.com",
	}
	for api, wantHost := range cases {
		t.Setenv("PARTYLINE_API", api)
		got := ConfigDir()
		want := filepath.Join(prod, "envs", wantHost)
		if got != want {
			t.Errorf("%s: got %s, want %s", api, got, want)
		}
		if got == prod {
			t.Errorf("%s: must NOT share the production directory", api)
		}
		if IsProd() {
			t.Errorf("%s: must not be production", api)
		}
	}
}

func TestEnvLabel(t *testing.T) {
	for api, want := range map[string]string{
		"https://staging.partyline.sh": "staging",
		"http://localhost:3111":        "localhost:3111",
		"https://partyline.sh":         "",
	} {
		t.Setenv("PARTYLINE_API", api)
		if got := EnvLabel(); got != want {
			t.Errorf("%s: label %q, want %q", api, got, want)
		}
	}
}

// A host becomes a path segment, and PARTYLINE_API is developer-supplied — so a hostile value must
// not be able to walk out of the config root.
func TestHostCannotEscapeConfigRoot(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".partyline")
	for _, api := range []string{
		"https://../../etc",
		"https://a/../../../b",
		"https://x%2F..%2F..",
	} {
		t.Setenv("PARFYLINE_UNUSED", "") // keep the loop honest if Setenv is ever reordered
		t.Setenv("PARTYLINE_API", api)
		got := filepath.Clean(ConfigDir())
		if !strings.HasPrefix(got, root+string(filepath.Separator)) && got != root {
			t.Errorf("%s escaped the config root: %s", api, got)
		}
	}
}
