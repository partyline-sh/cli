package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var updateBindings = flag.Bool("update-bindings", false, "rewrite testdata/tmux-bindings.golden from the current conf")

// confBindings is every line of the generated conf that binds a key, sets the prefix or sets a
// hook, with the binary path normalised. Styling can change freely; these cannot change by
// accident (docs/plans/ui-key-inventory.md).
func confBindings(clip string) string {
	// Fixed so the golden is the same on a Mac (pbcopy) and on Linux CI (often none); both
	// branches of the conf are compared, with and without a clipboard tool.
	saved := tmuxClipboard
	tmuxClipboard = func() string { return clip }
	defer func() { tmuxClipboard = saved }()
	var out []string
	for _, line := range strings.Split(tmuxConf(), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "bind") || strings.HasPrefix(l, "set -g prefix") || strings.HasPrefix(l, "set-hook") ||
			strings.Contains(l, "extended-keys") || strings.Contains(l, "terminal-features") || strings.HasPrefix(l, "set -g mouse") {
			out = append(out, strings.ReplaceAll(l, selfExe(), "PTLN"))
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func TestTmuxConfBindingsUnchanged(t *testing.T) {
	for golden, clip := range map[string]string{
		"testdata/tmux-bindings.golden":             "CLIPBOARD",
		"testdata/tmux-bindings-noclipboard.golden": "",
	} {
		got := confBindings(clip)
		if *updateBindings {
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (generate it from the current code with -update-bindings)", err)
		}
		if got != string(want) {
			t.Errorf("%s: the conf's key bindings changed; a binding change must be deliberate (update the golden and the key inventory).\ngot:\n%s\nwant:\n%s", golden, got, want)
		}
	}
}
