package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Updating the relay after partyline updates. The phone app is served by the relay, so a new
// partyline reaches the phone only when the relay is deployed again. The watcher restarts on the
// new binary (watcher.go) and calls relayAutoUpdate: when this machine deployed the relay, saved
// its Cloudflare token, and runs a version at least as new as the one deployed, it deploys again in
// the background. The outcome is kept in auto-update.json for the relay settings screen and status;
// the deploy's output goes to auto-update.log. One attempt per build per hour, so a deploy that
// keeps failing (no Node.js, a revoked token) is not retried on every watcher start.

type relayAutoResult struct {
	Sources string    `json:"sources"`
	At      time.Time `json:"at"`
	Error   string    `json:"error,omitempty"`
}

func relayAutoPath() string    { return filepath.Join(relayDir(), "auto-update.json") }
func relayAutoLogPath() string { return filepath.Join(relayDir(), "auto-update.log") }

func lastRelayAuto() (relayAutoResult, bool) {
	var r relayAutoResult
	b, err := os.ReadFile(relayAutoPath())
	return r, err == nil && json.Unmarshal(b, &r) == nil
}

// relayAutoWhy says why the relay will not update by itself, or "" when it will.
func relayAutoWhy(c relayConfig) string {
	switch {
	case !relayOutdated(c):
		return "current"
	case c.Sources == "":
		return "this machine did not deploy the relay"
	case savedRelayAPIToken() == "":
		return "no Cloudflare token is saved"
	case c.Version != "" && versionLess(version, c.Version):
		return "the relay runs a newer partyline (" + c.Version + ") than this one"
	}
	return ""
}

// runRelayDeploy runs `ptln relay deploy` for the background update; a variable for tests.
var runRelayDeploy = func(ctx context.Context, log *os.File) error {
	cmd := exec.CommandContext(ctx, selfExe(), "relay", "deploy")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, log, log
	return cmd.Run()
}

func relayAutoUpdate(ctx context.Context) {
	c, _, err := loadRelay()
	if err != nil || relayAutoWhy(c) != "" {
		return
	}
	target := relaySourcesDigest()
	if last, ok := lastRelayAuto(); ok && last.Sources == target && time.Since(last.At) < time.Hour {
		return
	}
	log, err := os.OpenFile(relayAutoLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer log.Close()
	fmt.Fprintf(log, "%s: updating the relay to partyline %s\n", time.Now().Format(time.RFC3339), version)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	result := relayAutoResult{Sources: target, At: time.Now().UTC()}
	if err := runRelayDeploy(ctx, log); err != nil {
		result.Error = err.Error()
	}
	b, _ := json.Marshal(result)
	_ = relayPrivateFile(relayAutoPath(), b)
}

// lockRelayDeploy keeps deploys on this machine one at a time, whether started by hand or by the
// background update; release with the returned file's Close.
func lockRelayDeploy() (*os.File, error) {
	if err := os.MkdirAll(relayDir(), 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(relayDir(), "deploy.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		return nil, fmt.Errorf("a relay deploy is already running on this machine")
	}
	return lock, nil
}

// relayAutoLine is the automatic-update line for status and the settings screen.
func relayAutoLine(c relayConfig) string {
	line := "on: a new partyline updates the relay by itself"
	if why := relayAutoWhy(c); why == "current" {
		line = "on"
	} else if why != "" {
		line = "off: " + why
	}
	if last, ok := lastRelayAuto(); ok && last.Error != "" {
		line += fmt.Sprintf("; the last try (%s) failed, see %s", last.At.Local().Format("Jan 2 15:04"), relayAutoLogPath())
	}
	return line
}
