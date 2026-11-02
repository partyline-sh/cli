package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func updateTestState(t *testing.T, latest string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CI", "")
	t.Setenv("PARTYLINE_NO_UPDATE_CHECK", "")
	t.Setenv("NO_UPDATE_NOTIFIER", "")
	old := version
	version = "0.111.0"
	t.Cleanup(func() { version = old })
	writeUpdateCache(latest, "* 1234567 loops: token use by model (#1498)\n")
}

// The popup opens once per release on an idle client, never on a busy one, and not again for a
// day after it opened; a skipped release never asks.
func TestUpdatePopupOpens(t *testing.T) {
	now := time.Now()
	idle := []gateClient{{Name: "/dev/ttys001", Activity: now.Add(-time.Minute), Width: 120, Height: 40}}
	busy := []gateClient{{Name: "/dev/ttys001", Activity: now.Add(-5 * time.Second), Width: 120, Height: 40}}
	opener := func(cs []gateClient, opened *int) *updateOpener {
		return &updateOpener{clients: func() ([]gateClient, error) { return cs, nil }, open: func(gateClient) error { *opened++; return nil },
			auto: func(string) error { t.Fatal("automatic updates are off"); return nil }}
	}
	t.Run("idle, then a day's wait", func(t *testing.T) {
		updateTestState(t, "0.112.0")
		opened := 0
		if !opener(idle, &opened).tick(now) || opened != 1 {
			t.Fatalf("did not open on an idle client")
		}
		if opener(idle, &opened).tick(now.Add(time.Hour)) || opened != 1 {
			t.Errorf("opened again within a day")
		}
		if !opener(idle, &opened).tick(now.Add(25*time.Hour)) || opened != 2 {
			t.Errorf("did not ask again the next day")
		}
	})
	t.Run("busy", func(t *testing.T) {
		updateTestState(t, "0.112.0")
		opened := 0
		if opener(busy, &opened).tick(now) || opened != 0 {
			t.Errorf("opened over someone typing")
		}
	})
	t.Run("skipped or current", func(t *testing.T) {
		updateTestState(t, "0.112.0")
		_ = saveUpdatePrefs(updatePrefs{Skip: "0.112.0"})
		opened := 0
		if opener(idle, &opened).tick(now) {
			t.Errorf("asked about a skipped release")
		}
		writeUpdateCache("0.113.0", "")
		if !opener(idle, &opened).tick(now) {
			t.Errorf("a newer release than the skipped one did not ask")
		}
		updateTestState(t, "0.111.0")
		if opener(idle, &opened).tick(now) {
			t.Errorf("asked with nothing newer")
		}
	})
}

// With automatic updates on the watcher installs the release once; the popup opens only when that
// failed, showing why.
func TestUpdateAutomatic(t *testing.T) {
	now := time.Now()
	idle := []gateClient{{Name: "c", Activity: now.Add(-time.Minute), Width: 120, Height: 40}}
	updateTestState(t, "0.112.0")
	_ = saveUpdatePrefs(updatePrefs{Auto: true})
	done := make(chan string, 2)
	opened := 0
	o := &updateOpener{clients: func() ([]gateClient, error) { return idle, nil }, open: func(gateClient) error { opened++; return nil },
		auto: func(v string) error { done <- v; return errors.New("brew exited 1") }}
	if o.tick(now) || opened != 0 {
		t.Fatal("the popup opened instead of updating")
	}
	if v := <-done; v != "0.112.0" {
		t.Fatalf("updated to %q", v)
	}
	deadline := time.Now().Add(2 * time.Second)
	for loadUpdatePrefs().AutoError == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !o.tick(now) || opened != 1 || len(done) != 0 {
		t.Errorf("after a failed automatic update: opened %d, tried again %d", opened, len(done))
	}
}

// The popup's rows: skip records the version, automatic is refused where it cannot run unattended.
func TestUpdatePromptRows(t *testing.T) {
	var skipped string
	act := updateActions{skip: func(v string) error { skipped = v; return nil }, autoOn: func(bool) error { return nil },
		canAuto: "Automatic updates need to write /usr/local/bin without a password", how: "the install script"}
	m := newUpdatePrompt("0.112.0", "0.111.0", []string{"loops: token use by model"}, false, act)
	down := func(m updatePromptModel) updatePromptModel {
		next, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
		return next.(updatePromptModel)
	}
	enter := func(m updatePromptModel) updatePromptModel {
		next, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		return next.(updatePromptModel)
	}
	auto := enter(down(down(m)))
	if auto.auto || !strings.Contains(auto.err, "without a password") {
		t.Errorf("automatic turned on where it cannot run: auto %v, %q", auto.auto, auto.err)
	}
	if s := enter(down(m)); skipped != "0.112.0" || s.state != "skipped" {
		t.Errorf("skip recorded %q, state %q", skipped, s.state)
	}
	if v := m.View().Content; !strings.Contains(v, "loops: token use by model") || !strings.Contains(v, "Update now") {
		t.Errorf("the popup lacks its changes or rows:\n%s", v)
	}
}
