package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeListClients puts a tmux on PATH whose list-clients prints out.
func fakeListClients(t *testing.T, out string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *list-clients*) printf '%s' \"$FAKE_CLIENTS\";; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CLIENTS", out)
}

// A client activity that does not parse reads as active now, never as idle: idle is what lets the
// approval popup open over someone.
func TestGateClientsUnparsedActivityIsNotIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeListClients(t, "/dev/ttys001\tnot-a-number\t120\t40\n")
	cs, err := tmuxClients()
	if err != nil || len(cs) != 1 {
		t.Fatalf("clients %v, err %v", cs, err)
	}
	if time.Since(cs[0].Activity) > time.Minute {
		t.Errorf("an unparsed activity looks idle: %v", cs[0].Activity)
	}
}

// The indicators are reconciled once per refresh, however many runs wait.
func TestRefreshIndicatorsReconcilesOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < 3; i++ {
		waitingRun(t, loopAwaiting)
	}
	calls := 0
	old := markWaiting
	t.Cleanup(func() { markWaiting = old })
	markWaiting = func(*loopRun) { calls++ }
	loopRefreshIndicators()
	if calls != 1 {
		t.Errorf("reconciled %d times for 3 waiting runs", calls)
	}
}

// A chat popup's covered pane counts as unwatched only when its client is the only one: a second
// client may be showing that pane live.
func TestPaneCoveredByChatNeedsOneClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = os.WriteFile(chatPopupPIDPath(), []byte(fmt.Sprintf("%d %%5", os.Getpid())), 0o600)
	fakeListClients(t, "%7\n")
	if paneWatched("%5") {
		t.Error("one client, the pane covered by its chat popup: still counted as watched")
	}
	fakeListClients(t, "%7\n%5\n")
	if !paneWatched("%5") {
		t.Error("a second client showing the covered pane did not count as watching it")
	}
}
