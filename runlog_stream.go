// crank-01, LIVE STEP OUTPUT — producer side. The runReporter (crank.go) posts the LOW-volume,
// hash-chained milestone ledger; this is its HIGH-volume, NON-chained twin: the worker's actual
// stdout/step stream, batched and posted to run_logs (0055) so the run detail page tails it live over
// Realtime. Deliberately separate from the chain (logs would bloat it). Best-effort telemetry: a
// dropped batch is a few missing lines, never a failed run.
package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"partyline.sh/partyline/internal/api"
)

const (
	logFlushInterval = 750 * time.Millisecond // tail latency for the live stream
	logBatchMax      = 500                    // server caps a batch at 500 lines — chunk to match
)

// runLogger buffers step-output lines and flushes them to the run store on a timer. Safe for
// concurrent append from multiple worker goroutines (claim mode). A logger with no post func (missing
// run id / device token) is an inert no-op: append does nothing and sink returns nil, so runWorker
// stays on its buffered (non-streaming) path when there's nowhere to send logs.
type runLogger struct {
	post func([]api.RunLogLine)
	seq  atomic.Int64

	mu   sync.Mutex
	buf  []api.RunLogLine
	stop chan struct{}
	done chan struct{}
}

// newRunLogger wires a live logger when crank has a run id AND the daemon exposed the device token via
// env (the same credentials newRunReporter uses). Missing either → an inert logger. The flush goroutine
// runs until close().
func newRunLogger(runID string) *runLogger {
	if runID == "" {
		return &runLogger{}
	}
	token := strings.TrimSpace(os.Getenv("PARTYLINE_DAEMON_TOKEN"))
	if token == "" {
		return &runLogger{}
	}
	base := strings.TrimSpace(os.Getenv("PARTYLINE_API"))
	return newRunLoggerWith(base, token, runID)
}

// newRunLoggerWith builds a live logger from EXPLICIT credentials. A crank child reads its device token
// from env (newRunLogger); the daemon process holds d.Base/d.Token directly (the describe job runs
// in-process, not as a child), so it wires the logger this way. Empty run id/token → an inert logger.
func newRunLoggerWith(base, token, runID string) *runLogger {
	l := &runLogger{}
	if runID == "" || strings.TrimSpace(token) == "" {
		return l
	}
	if strings.TrimSpace(base) == "" {
		base = api.Base()
	}
	l.post = func(batch []api.RunLogLine) {
		if err := api.AppendRunLogs(base, token, runID, batch); err != nil {
			fmt.Fprintf(os.Stderr, "  (run-log stream: %v)\n", err)
		}
	}
	l.stop = make(chan struct{})
	l.done = make(chan struct{})
	go l.loop()
	return l
}

// active reports whether this logger will actually ship lines (has credentials + a run id).
func (l *runLogger) active() bool { return l != nil && l.post != nil }

// sink returns the onLog closure runWorker calls per streamed step line for task idx, or nil when the
// logger is inert (so runWorker uses the buffered path). The returned closure is safe for concurrent
// use.
func (l *runLogger) sink(idx int) func(string) {
	if !l.active() {
		return nil
	}
	taskIdx := idx
	return func(line string) { l.append(&taskIdx, "stdout", line) }
}

// note appends a run-level (task_idx nil) marker line — e.g. a task header — to the stream.
func (l *runLogger) note(idx int, stream, line string) {
	if !l.active() {
		return
	}
	taskIdx := idx
	l.append(&taskIdx, stream, line)
}

func (l *runLogger) append(taskIdx *int, stream, body string) {
	if !l.active() || strings.TrimSpace(body) == "" {
		return
	}
	line := api.RunLogLine{TaskIdx: taskIdx, Seq: l.seq.Add(1), Stream: stream, Body: body}
	l.mu.Lock()
	l.buf = append(l.buf, line)
	l.mu.Unlock()
}

func (l *runLogger) loop() {
	defer close(l.done)
	t := time.NewTicker(logFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.flush()
		case <-l.stop:
			l.flush()
			return
		}
	}
}

// flush ships whatever's buffered, chunked to the server's per-batch cap.
func (l *runLogger) flush() {
	l.mu.Lock()
	batch := l.buf
	l.buf = nil
	l.mu.Unlock()
	for len(batch) > 0 {
		n := len(batch)
		if n > logBatchMax {
			n = logBatchMax
		}
		l.post(batch[:n])
		batch = batch[n:]
	}
}

// close stops the flush loop after a final flush. Idempotent-safe for an inert logger (no-op).
func (l *runLogger) close() {
	if !l.active() {
		return
	}
	close(l.stop)
	<-l.done
}
