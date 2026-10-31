package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestTmuxRequired(t *testing.T) {
	old := probeTmuxVersion
	t.Cleanup(func() { probeTmuxVersion = old })
	for _, found := range []string{"no tmux on PATH", "tmux 3.2a", "tmux 3.1", "tmux version unavailable"} {
		t.Run(found, func(t *testing.T) {
			probeTmuxVersion = func() string { return found }
			for name, run := range map[string]func() error{
				"requirement":   requireTmux,
				"bare":          runTmuxBare,
				"launcher":      func() error { return runLLMSApp(nil) },
				"sessions":      func() error { return runLLMSApp([]Spec{{Key: "test"}}) },
				"tmux sessions": func() error { return runTmuxApp([]Spec{{Key: "test"}}) },
				"home":          tmuxHome,
			} {
				t.Run(name, func(t *testing.T) {
					err := run()
					if err == nil {
						t.Fatal("accepted an unavailable or old tmux")
					}
					for _, want := range []string{"tmux 3.3 or newer", found, "brew install tmux", "distro package manager"} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("error %q missing %q", err, want)
						}
					}
				})
			}
		})
	}
	for _, found := range []string{"tmux 3.3", "tmux 3.3a", "tmux 3.7c", "tmux next-3.6"} {
		probeTmuxVersion = func() string { return found }
		if err := requireTmux(); err != nil {
			t.Errorf("%s: %v", found, err)
		}
	}
}

func TestTmuxRequiredExit(t *testing.T) {
	if found := os.Getenv("PTLN_TEST_TMUX_VERSION"); found != "" {
		probeTmuxVersion = func() string { return found }
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"ptln"}, os.Args[i+1:]...)
				break
			}
		}
		main()
		os.Exit(0)
	}
	for _, found := range []string{"no tmux on PATH", "tmux 3.2a"} {
		// Bare `ptln` is not in the list: with no terminal (as here) it prints help, tmux or not.
		for _, args := range [][]string{{"new", "claude"}, {"llms", "--resume"}, {"llms", "some-session"}, {"tmux", "--home"}} {
			t.Run(found+"/"+strings.Join(args, " "), func(t *testing.T) {
				cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestTmuxRequiredExit$", "--"}, args...)...)
				cmd.Env = append(os.Environ(), "PTLN_TEST_TMUX_VERSION="+found, "HOME="+t.TempDir(), "DO_NOT_TRACK=1")
				out, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("accepted missing or old tmux: %s", out)
				}
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
					t.Fatalf("unexpected exit: %v: %s", err, out)
				}
				for _, want := range []string{found, "tmux 3.3 or newer", "brew install tmux", "distro package manager"} {
					if !strings.Contains(string(out), want) {
						t.Errorf("output %q missing %q", out, want)
					}
				}
			})
		}
	}
}
