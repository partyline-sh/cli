package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGatewayServiceStartCanRetryAfterRegistrationFailure(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("user services unsupported")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	commands := filepath.Join(home, "commands")
	failure := filepath.Join(home, "fail")
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shQuote(commands) + "\nif [ -f " + shQuote(failure) + " ]; then exit 17; fi\n"
	for _, name := range []string{"launchctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	if _, err := initializeRelayHost(relayHostDir(), relayHostConfig{Origin: "https://test.invalid", RelayOnly: true}); err != nil {
		t.Fatal(err)
	}
	spec, err := relayServiceSpecCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(failure, nil, 0600); err != nil {
		t.Fatal(err)
	}
	err = relayServiceStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exit status 17") {
		t.Fatal("registration error lost", err)
	}
	if _, err = os.Lstat(spec.Path); !os.IsNotExist(err) {
		t.Fatal("failed service definition blocks retry", err)
	}
	if _, err = os.Stat(filepath.Join(relayHostDir(), "server.json")); err != nil {
		t.Fatal("failure removed gateway settings", err)
	}
	if err = os.Remove(failure); err != nil {
		t.Fatal(err)
	}
	if err = relayServiceStart(context.Background()); err != nil {
		t.Fatal("retry failed", err)
	}
	info, err := os.Stat(spec.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("service definition not private", err)
	}
	before, err := os.ReadFile(commands)
	if err != nil {
		t.Fatal(err)
	}
	if err = relayServiceStart(context.Background()); err == nil {
		t.Fatal("existing service replaced")
	}
	after, _ := os.ReadFile(commands)
	if string(before) != string(after) {
		t.Fatal("existing service was registered again")
	}
}

func TestGatewayServiceUsesOnlyMatchingInstalledExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "real-ptln")
	if err := os.WriteFile(exe, []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "ptln")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := relayServiceExecutable(exe); got != link {
		t.Fatalf("stable installation link not kept: %q", got)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("different binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := relayServiceExecutable(exe); got != exe {
		t.Fatalf("unrelated ptln replaced running binary: %q", got)
	}
}

func TestGatewayServiceStopRecoversUnloadedServiceButKeepsRealFailures(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("user services unsupported")
	}
	missing := "Failed to stop partyline-relay.service: Unit partyline-relay.service not loaded."
	if runtime.GOOS == "darwin" {
		missing = "Boot-out failed: 3: No such process"
	}
	for _, tc := range []struct {
		name, output string
		removed      bool
	}{
		{"not-loaded", missing, true},
		{"permission", "Permission denied", false},
		{"missing-user-bus", "Failed to connect to bus: No such file or directory", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\ncase \"$*\" in *daemon-reload*) exit 0;; esac\nprintf '%s\\n' " + shQuote(tc.output) + "\nexit 1\n"
			for _, name := range []string{"launchctl", "systemctl"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			if _, err := initializeRelayHost(relayHostDir(), relayHostConfig{Origin: "https://test.invalid", RelayOnly: true}); err != nil {
				t.Fatal(err)
			}
			spec, err := relayServiceSpecCurrent()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(spec.Path), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(spec.Path, []byte(spec.Contents), 0600); err != nil {
				t.Fatal(err)
			}
			err = relayServiceMain([]string{"stop"})
			if tc.removed {
				if err != nil {
					t.Fatal("unloaded service could not be removed", err)
				}
				if _, err = os.Lstat(spec.Path); !os.IsNotExist(err) {
					t.Fatal("stale definition retained", err)
				}
				if err = relayServiceMain([]string{"stop"}); err != nil {
					t.Fatal("second stop should also succeed", err)
				}
				for _, name := range []string{"launchctl", "systemctl"} {
					if err = os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err = relayServiceStart(context.Background()); err != nil {
					t.Fatal("start after stop failed", err)
				}
			} else {
				if err == nil {
					t.Fatal("real manager failure was hidden")
				}
				b, readErr := os.ReadFile(spec.Path)
				if readErr != nil || string(b) != spec.Contents {
					t.Fatal("definition removed while service might still be running", readErr)
				}
			}
		})
	}
}
