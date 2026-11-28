package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Store keeps run records on disk, so history survives closing a run's tab or restarting
// partyline. One directory per run: run.json (the record), output.log, and the result directory.
//
//	<root>/<job-key>/<run-id>/run.json
type Store struct{ Root string }

// DefaultStore is ~/.partyline/jobs/runs.
func DefaultStore() (Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Store{}, err
	}
	return Store{Root: filepath.Join(home, ".partyline", "jobs", "runs")}, nil
}

// jobKey turns a job ID ("github.com/o/r#news") into a safe directory name that stays readable.
func jobKey(jobID string) string {
	sum := sha256.Sum256([]byte(jobID))
	name := jobID
	if h := strings.LastIndexByte(name, '#'); h >= 0 {
		name = name[h+1:]
	}
	return name + "-" + hex.EncodeToString(sum[:])[:12]
}

// runIDOK: a run ID is one plain path element, so no record can be written outside its job's directory.
var runIDOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validRunID(id string) error {
	if !runIDOK.MatchString(id) || strings.Contains(id, "..") {
		return fmt.Errorf("run id %q is not a plain name", id)
	}
	return nil
}

// Dir is a run's directory. Callers validate the run ID first (Prepare, Save and Get all do).
func (s Store) Dir(jobID, runID string) string { return filepath.Join(s.Root, jobKey(jobID), runID) }

// Prepare creates a run's directory and fills in its log and result paths.
func (s Store) Prepare(r *Run) error {
	if r.JobID == "" {
		return errors.New("run needs a job id")
	}
	if err := validRunID(r.ID); err != nil {
		return err
	}
	dir := s.Dir(r.JobID, r.ID)
	if err := os.MkdirAll(filepath.Join(dir, "result"), 0o700); err != nil {
		return err
	}
	r.LogPath, r.ResultDir = filepath.Join(dir, "output.log"), filepath.Join(dir, "result")
	return nil
}

// Save writes a run record atomically.
func (s Store) Save(r Run) error {
	if err := validRunID(r.ID); err != nil {
		return err
	}
	dir := s.Dir(r.JobID, r.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "run.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "run.json"))
}

// Get reads one run.
func (s Store) Get(jobID, runID string) (Run, error) {
	if err := validRunID(runID); err != nil {
		return Run{}, ErrNoRun
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(jobID, runID), "run.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Run{}, ErrNoRun
	} else if err != nil {
		return Run{}, err
	}
	var r Run
	return r, json.Unmarshal(b, &r)
}

// List returns a job's runs, newest first. A record that cannot be read is skipped, not fatal:
// one damaged file must not hide the rest of the history.
func (s Store) List(jobID string) ([]Run, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, jobKey(jobID)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []Run
	for _, e := range entries {
		if r, err := s.Get(jobID, e.Name()); err == nil && e.IsDir() {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	return out, nil
}

// ReadResult reads and validates the agent's result file from a run's result directory.
func ReadResult(r Run, token string) (Result, error) {
	b, err := os.ReadFile(filepath.Join(r.ResultDir, "result.json"))
	if err != nil {
		return Result{}, err
	}
	var res Result
	if err := json.Unmarshal(b, &res); err != nil {
		return Result{}, err
	}
	return res, res.Validate(token)
}
