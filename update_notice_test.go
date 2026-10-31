package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The launcher says when a newer release is cached, and stays quiet when up to date, opted out,
// or a dev build; the watcher's step refreshes a day-old cache at most once an hour.
func TestUpdateNotice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CI", "")
	t.Setenv("PARTYLINE_NO_UPDATE_CHECK", "")
	t.Setenv("NO_UPDATE_NOTIFIER", "")
	old := version
	t.Cleanup(func() { version = old })
	version = "0.108.0"
	write := func(latest string, at time.Time) {
		_ = os.MkdirAll(stateDir(), 0o700)
		b, _ := json.Marshal(updateCache{CheckedAt: at, Latest: latest})
		_ = os.WriteFile(updateCachePath(), b, 0o600)
	}
	write("0.109.0", time.Now())
	if got := updateAvailable(); !strings.Contains(got, "ptln 0.109.0 is available (you're on 0.108.0)") || !strings.Contains(got, "ptln upgrade") {
		t.Errorf("behind: %q", got)
	}
	a := &aiMenu{updateLine: updateAvailable()}
	if h := gateANSI.ReplaceAllString(launcherHeader(a, gateStyle{tmuxTheme()}), ""); !strings.Contains(h, "0.109.0 is available") {
		t.Errorf("the launcher header lacks the notice: %q", h)
	}
	write("0.108.0", time.Now())
	if got := updateAvailable(); got != "" {
		t.Errorf("up to date: %q", got)
	}
	write("0.109.0", time.Now())
	t.Setenv("PARTYLINE_NO_UPDATE_CHECK", "1")
	if got := updateAvailable(); got != "" {
		t.Errorf("opted out: %q", got)
	}
	t.Setenv("PARTYLINE_NO_UPDATE_CHECK", "")
	version = "dev"
	if got := updateAvailable(); got != "" {
		t.Errorf("dev build: %q", got)
	}

	// The watcher's step: a fresh cache is not refreshed, and it looks at most once an hour.
	version = "0.108.0"
	oldURL := releasesLatestURL
	t.Cleanup(func() { releasesLatestURL = oldURL })
	releasesLatestURL = "http://127.0.0.1:1/never" // no network in the test: a refresh would fail quietly
	write("0.109.0", time.Now())
	lastUpdateLook = time.Time{}
	watchForUpdates()
	if time.Since(lastUpdateLook) > time.Minute {
		t.Error("the step did not record its look")
	}
	before := lastUpdateLook
	watchForUpdates()
	if !lastUpdateLook.Equal(before) {
		t.Error("the step looked again within the hour")
	}
}
