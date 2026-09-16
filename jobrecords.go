package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"partyline.sh/partyline/internal/api"
)

// jobrecords.go — crash-honesty for IN-PROCESS jobs (presets describe / review / rebase).
//
// Crank runs are detached children with a pid record, so sweepOrphanRuns can reconcile them
// after a crash. Jobs are goroutines that die WITH the daemon: a restart mid-job (self-update,
// upgrade, crash) strands the run `running`/`accepted` server-side forever — and a stranded
// review additionally blocks every future "Request review" for its target via the one-in-flight
// idempotency check. Mirror each job to disk while it runs; at STARTUP, before the stream can
// dispatch anything new, fail whatever the server still believes is in flight with an honest
// reason. Startup-only by design: a live daemon must never sweep records its own running jobs
// just wrote.

func jobRecordsPath() string { return filepath.Join(daemonDir(), "jobs.json") }

var jobRecMu sync.Mutex

func loadJobRecords() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(jobRecordsPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveJobRecords(m map[string]string) {
	if err := os.MkdirAll(filepath.Dir(jobRecordsPath()), 0o700); err != nil {
		return
	}
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(jobRecordsPath(), b, 0o600)
	}
}

// runJob runs one in-process job with its lifetime mirrored to disk (runID → preset). The record
// exists exactly while the job runs; a record found at the NEXT startup is proof the daemon died
// mid-job.
func runJob(d daemonDevice, ev api.RunEvent, fn func(daemonDevice, api.RunEvent) error) error {
	jobRecMu.Lock()
	recs := loadJobRecords()
	recs[ev.RunID] = ev.Preset
	saveJobRecords(recs)
	jobRecMu.Unlock()
	defer func() {
		jobRecMu.Lock()
		recs := loadJobRecords()
		delete(recs, ev.RunID)
		saveJobRecords(recs)
		jobRecMu.Unlock()
	}()
	return fn(d, ev)
}

// sweepOrphanJobs reconciles jobs the previous daemon process died holding. Called once at
// startup, before the stream connects — every record on disk is from a dead process, so unlike
// sweepOrphanRuns there is no pid to check. Only a run the server still shows in flight is
// touched (a job that reported its terminal status before the crash is left alone); the failure
// reason names the restart so the operator knows to just request it again.
func sweepOrphanJobs(d daemonDevice) {
	jobRecMu.Lock()
	defer jobRecMu.Unlock()
	recs := loadJobRecords()
	if len(recs) == 0 {
		return
	}
	for runID, preset := range recs {
		status, err := api.RunStatus(d.Base, d.Token, runID)
		if err == nil && (status == "running" || status == "accepted") {
			_ = api.SetRunStatus(d.Base, d.Token, runID, "failed",
				fmt.Sprintf("%s: daemon restarted mid-job — request it again", preset))
			fmt.Printf("↻ orphaned %s job %s (daemon restarted mid-job) — marked failed\n", preset, runID)
		}
	}
	saveJobRecords(map[string]string{})
}
