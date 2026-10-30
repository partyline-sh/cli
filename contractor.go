package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Contractors: the agents partyline starts for a loop (a builder, the arbiter, a reviewer), each
// headless, each in a window of a second tmux session (contractorSession) so the person's tabs
// never grow, each tied to the session whose loop it serves (Parent). Loops are the only callers:
// there is no command that starts one.

// contractorSession must not start with "ptln": tmux matches a session target by prefix when no
// session has the exact name, so with only this session left every `-t ptln` would land in it.
const contractorSession = "contractors"

// contractor is one run's record, ~/.partyline/contractors/<id>.json.
type contractor struct {
	ID      string    `json:"id"`
	Parent  string    `json:"parent"` // the session key whose loop it serves ("pane:%18")
	Loop    string    `json:"loop,omitempty"`
	Round   int       `json:"round,omitempty"`
	Role    string    `json:"role"`   // builder, reviewer, arbiter
	Engine  string    `json:"engine"` // engine:model, for the chip
	Title   string    `json:"title"`  // what it is doing, in a few words
	Dir     string    `json:"dir"`
	Argv    []string  `json:"argv"`
	Env     []string  `json:"env,omitempty"` // the whole environment it runs with, when set
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	State   string    `json:"state"` // running, done, failed
	Exit    int       `json:"exit"`
	Result  string    `json:"result,omitempty"` // one line: its last line of output, or why it failed
	Seen    bool      `json:"seen,omitempty"`   // a finished chip has been opened or its parent selected
	Window  string    `json:"window,omitempty"`
}

func contractorsDir() string          { return filepath.Join(stateDir(), "contractors") }
func contractorPath(id string) string { return filepath.Join(contractorsDir(), id+".json") }
func contractorLog(id string) string  { return filepath.Join(contractorsDir(), id+".log") }

func (c *contractor) save() error {
	if err := os.MkdirAll(contractorsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// A temporary file of its own: the runner, the watcher and a hook can each be saving.
	f, err := os.CreateTemp(contractorsDir(), c.ID+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return werr
	}
	return os.Rename(f.Name(), contractorPath(c.ID))
}

// withContractors serialises every read-modify-write of the records across processes (the
// runner, the watcher, the status hooks), so a finished record is never overwritten by a stale one.
func withContractors(fn func() error) error {
	if err := os.MkdirAll(contractorsDir(), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(contractorsDir(), ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

// updateContractor re-reads id under the lock and saves what fn makes of it; fn returns false to
// leave the record as it is.
func updateContractor(id string, fn func(c *contractor) bool) error {
	return withContractors(func() error {
		c, err := loadContractor(id)
		if err != nil {
			return err
		}
		if !fn(c) {
			return nil
		}
		return c.save()
	})
}

func loadContractor(id string) (*contractor, error) {
	b, err := os.ReadFile(contractorPath(id))
	if err != nil {
		return nil, err
	}
	var c contractor
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// loadContractors is every record, oldest first.
func loadContractors() []*contractor {
	files, _ := filepath.Glob(filepath.Join(contractorsDir(), "*.json"))
	var out []*contractor
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		// The id reaches tmux formats and a run-shell through the row's click ranges: a record whose
		// id is not one startContractor would make, or is not its file's name, is skipped.
		if c, err := loadContractor(id); err == nil && c.ID == id && contractorID.MatchString(id) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// newContractorID is a fresh contractor id: 7 bytes, so it fits a tmux status range name (15).
func newContractorID() string {
	b := make([]byte, 7)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// stopContractor closes a running contractor's window; its runner ends the agent's process group.
var stopContractor = func(id string) {
	if c, err := loadContractor(id); err == nil && c.State == "running" && c.Window != "" {
		_ = tmuxCmd("kill-window", "-t", c.Window).Run()
	}
}

// contractorID is the shape of an id: it becomes a file name and a tmux status range, whose name
// tmux keeps to 15 characters.
var contractorID = regexp.MustCompile(`^[0-9a-f]{6,14}$`)

// startContractor records c and starts it in a window of the contractor session, which is created
// with its first contractor. The window runs `ptln _contractor <id>`, which waits until its window
// is recorded and then runs c.Argv. The record, the session and the window are made under the
// records lock, which reconcile also holds while it decides to kill the session, so a reconcile
// never sees a start half done; a "starting" record it does see was abandoned.
var startContractor = func(c *contractor) error {
	if c.ID == "" {
		c.ID = newContractorID()
	}
	if !contractorID.MatchString(c.ID) {
		return fmt.Errorf("contractor id %q is not lower-case hex", c.ID)
	}
	var openErr error
	err := withContractors(func() error {
		if _, err := os.Stat(contractorPath(c.ID)); err == nil {
			return fmt.Errorf("contractor %s already exists", c.ID)
		}
		c.State, c.Started = "starting", time.Now().UTC()
		if err := c.save(); err != nil {
			return err
		}
		run := []string{selfExe(), "_contractor", c.ID}
		args := []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-t", contractorSession + ":", "-n", c.ID, "-c", c.Dir, "--"}
		if !contractorSessionLive() {
			args = []string{"new-session", "-d", "-P", "-F", "#{window_id}", "-s", contractorSession, "-n", c.ID, "-c", c.Dir, "--"}
		}
		out, err := tmuxCmd(append(args, run...)...).Output()
		if err != nil {
			openErr = err
			c.State, c.Result, c.Ended = "failed", "could not open its window: "+err.Error(), time.Now().UTC()
		} else {
			c.State, c.Window = "running", strings.TrimSpace(string(out))
		}
		return c.save()
	})
	if err != nil {
		return err
	}
	if openErr != nil {
		contractorTellLoop(c)
	}
	contractorsReconcile()
	return openErr
}

// contractorStartWait is how long the runner waits for its record to leave "starting".
var contractorStartWait = 10 * time.Second

// contractorMain is `ptln _contractor <id>`, inside the contractor's window: it waits for its
// window to be recorded, runs the argv with its output to the log and the pane, records how it
// ended, and tells the loop.
func contractorMain(id string) {
	var c *contractor
	for deadline := time.Now().Add(contractorStartWait); ; time.Sleep(50 * time.Millisecond) {
		got, err := loadContractor(id)
		if err == nil && got.State != "starting" {
			c = got
			break
		}
		if time.Now().After(deadline) {
			var failed *contractor
			_ = updateContractor(id, func(r *contractor) bool {
				if r.State != "starting" {
					return false
				}
				r.State, r.Ended, r.Exit, r.Result = "failed", time.Now().UTC(), -1, "its window was never recorded"
				failed = r
				return true
			})
			if failed != nil {
				contractorTellLoop(failed)
			}
			fmt.Fprintln(os.Stderr, "ptln _contractor: its record never left starting")
			contractorsReconcile()
			return
		}
	}
	if c.State != "running" || len(c.Argv) == 0 {
		return
	}
	log, err := os.OpenFile(contractorLog(id), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ptln _contractor:", err)
		finishContractor(c.ID, err, "could not open its log: "+err.Error())
		return
	}
	defer log.Close()
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = c.Env
	}
	// Its own process group, so a hangup can end the agent and whatever it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	last := make(chan string, 1)
	go func() { last <- copyKeepingLastLine(pr, io.MultiWriter(log, paneWriter{os.Stdout})) }()
	// A hangup while the agent runs is a quit or a killed server: the agent goes with it. One after
	// it ended is the reconcile that follows our own finish killing the session; the runner must
	// survive that to finish writing. Caught, not ignored: an ignored signal stays ignored in the child.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	if err = cmd.Start(); err != nil {
		pw.Close()
		<-last
		finishContractor(c.ID, err, "")
		return
	}
	// On a hangup: SIGTERM to the group, then SIGKILL to whatever of it is left after 5 s, whether or
	// not the agent itself has exited; the runner waits for that before it records the end.
	exited, stopped := make(chan struct{}), make(chan struct{})
	var hungUp atomic.Bool
	go func() {
		defer close(stopped)
		select {
		case <-hup:
		case <-exited:
			return
		}
		hungUp.Store(true)
		group := -cmd.Process.Pid
		_ = syscall.Kill(group, syscall.SIGTERM)
		for deadline := time.Now().Add(5 * time.Second); syscall.Kill(group, 0) == nil; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(group, syscall.SIGKILL)
				return
			}
		}
	}()
	err = cmd.Wait()
	close(exited)
	<-stopped
	pw.Close()
	line := <-last
	if hungUp.Load() {
		// Interrupted, whatever the agent's exit status says.
		line = "stopped: its tmux session ended"
		err = fmt.Errorf("%s", line)
	}
	finishContractor(c.ID, err, line)
}

// paneWriter writes to the pane and ignores its errors: once the pane is gone the log must still
// get everything.
type paneWriter struct{ w io.Writer }

func (p paneWriter) Write(b []byte) (int, error) { _, _ = p.w.Write(b); return len(b), nil }

// copyKeepingLastLine copies r to w and returns the last non-blank line, at most 500 bytes of it;
// no line is too long to pass through, so the child never blocks on a full pipe.
func copyKeepingLastLine(r io.Reader, w io.Writer) string {
	br := bufio.NewReaderSize(r, 64<<10)
	last, cur := "", []byte{}
	for {
		chunk, err := br.ReadSlice('\n')
		_, _ = w.Write(chunk)
		if len(cur) < 500 {
			cur = append(cur, chunk[:min(len(chunk), 500-len(cur))]...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if t := strings.TrimSpace(string(cur)); t != "" {
			last = t
		}
		cur = cur[:0]
		if err != nil {
			return last
		}
	}
}

// finishContractor records how a contractor ended, tells its loop, and reconciles the row.
func finishContractor(id string, runErr error, last string) {
	var done *contractor
	_ = updateContractor(id, func(c *contractor) bool {
		c.Ended, c.State, c.Exit, c.Result = time.Now().UTC(), "done", 0, termSafe(last)
		if runErr != nil {
			c.State, c.Exit = "failed", 1
			if exit, ok := runErr.(*exec.ExitError); ok {
				c.Exit = exit.ExitCode()
			}
			if c.Result == "" {
				c.Result = runErr.Error()
			}
		}
		done = c
		return true
	})
	if done != nil {
		contractorTellLoop(done)
	}
	contractorsReconcile()
}

// contractorTellLoop posts how a contractor ended to its loop's inbox.
// Builders, reviewers and the arbiter hand their results to the loop themselves (loopBuilderDone,
// the round reading the review, applyArbiter), and the watcher restarts a builder that ended
// without doing so (buildersResume): the session is not woken for any of them.
func contractorTellLoop(c *contractor) {
	if c.Loop == "" || c.Role == "builder" || c.Role == "reviewer" || c.Role == "arbiter" {
		return
	}
	mark := "✓"
	if c.State != "done" {
		mark = "✗"
	}
	_ = loopPost(c.Loop, fmt.Sprintf("%s contractor %s (%s, %s) %s: %s", mark, c.ID, c.Role, c.Engine, c.State, c.Result))
}

// contractorAlive reports whether a running record's window still exists; a record whose window is
// gone (a crash, a killed server) is not running any more.
var contractorAlive = func(c *contractor) bool {
	if c.Window == "" {
		return false
	}
	// The id that comes back, not the exit status: tmux answers a target that no longer exists with
	// another window rather than an error.
	out, err := tmuxCmd("display-message", "-p", "-t", c.Window, "#{window_id}").Output()
	return err == nil && strings.TrimSpace(string(out)) == c.Window
}

// contractorSessionLive is whether the contractor session exists.
func contractorSessionLive() bool {
	return tmuxCmd("has-session", "-t", "="+contractorSession).Run() == nil
}

// killContractorSession ends the session once nothing runs in it, so it never keeps the server
// alive after the person's last window closes.
var killContractorSession = func(running int) {
	if running == 0 && contractorSessionLive() {
		_ = tmuxCmd("kill-session", "-t", "="+contractorSession).Run()
	}
}
