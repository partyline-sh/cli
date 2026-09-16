package main

import (
	"os"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// The job record must exist exactly while the job runs — that window is what makes a record
// found at the next startup proof of a death mid-job.
func TestRunJobRecordsLifetime(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // stateDir() derives from the home dir

	ev := api.RunEvent{RunID: "run-1", Preset: "review"}
	var during map[string]string
	err := runJob(daemonDevice{}, ev, func(daemonDevice, api.RunEvent) error {
		during = loadJobRecords()
		return nil
	})
	if err != nil {
		t.Fatalf("runJob: %v", err)
	}
	if during["run-1"] != "review" {
		t.Fatalf("record missing while job ran: %v", during)
	}
	if after := loadJobRecords(); len(after) != 0 {
		t.Fatalf("record not cleared after job: %v", after)
	}
}

// A job that fails must still clear its record — the server already has its terminal status, so
// the next startup has nothing to reconcile.
func TestRunJobClearsRecordOnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ev := api.RunEvent{RunID: "run-2", Preset: "describe"}
	_ = runJob(daemonDevice{}, ev, func(daemonDevice, api.RunEvent) error {
		return os.ErrDeadlineExceeded
	})
	if after := loadJobRecords(); len(after) != 0 {
		t.Fatalf("record not cleared after failing job: %v", after)
	}
}
