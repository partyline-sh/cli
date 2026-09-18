package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// The boot splash: what the terminal shows while the switchboard is starting up.
//
// Nothing used to paint before the work started — runLLMSApp ran loadTheme → collectSessions →
// loadLLMMeta → firstLauncherRun and only drew afterwards, so a slow launch (above all
// `ptln --resume` with several sessions to reopen) sat on a blank terminal with no surface to
// report onto. This file is the surface: a PURE step model plus a render function over
// (steps, cols, rows), so what is shown can be tested without a terminal, and a thin live
// display that drives it on a ticker.
//
// Display only. It never reorders, parallelises or skips any of the boot work — each step is
// marked as the step that was already happening completes.

// bootThreshold is how long loading must run before anything is painted. Below it a launch is
// perceived as instant, and a splash that flashes up and vanishes reads as a glitch. There is
// deliberately NO minimum display time on the other side: the splash is torn down the moment
// the work is done, never held open for effect.
const bootThreshold = 150 * time.Millisecond

// bootTick is how often the live display repaints (the wordmark gradient sweeps on it).
const bootTick = 80 * time.Millisecond

type bootState int

const (
	bootPending bootState = iota // not started
	bootRunning                  // happening right now
	bootDone                     // finished, carries its elapsed time
)

// bootStep is one line of the splash. Detail is the user-facing result of the step once it is
// known ("41 found"); Elapsed is filled when the step completes, so a slow launch names its own
// slow part.
type bootStep struct {
	Label   string
	Detail  string
	State   bootState
	Elapsed time.Duration

	startAt time.Duration // when this step began, measured from the start of boot
}

// bootModel is the whole splash as data: the steps so far plus how long boot has been running.
// Every mutator takes the elapsed-since-boot to use rather than reading a clock, so the model is
// pure and a test can drive it frame by frame.
type bootModel struct {
	Steps     []bootStep
	Elapsed   time.Duration // since boot began — compared against Threshold
	Threshold time.Duration
	Phase     int // gradient sweep position, advanced once per tick

	// Total is how many steps the launch expects, for the progress bar. Zero means "not known
	// yet", and the bar sweeps instead of filling — an honest indeterminate rather than a
	// fraction invented from the steps that happen to have started.
	Total int
}

// SetTotal raises the expected step count. It only ever grows: the fixed launch steps are known at
// the start, the session restores are not known until the workspace has been read, and a bar that
// jumped backwards would read as a stall.
func (m *bootModel) SetTotal(n int) {
	if n > m.Total {
		m.Total = n
	}
}

// doneCount is how many steps have finished, for the bar.
func (m *bootModel) doneCount() int {
	n := 0
	for _, s := range m.Steps {
		if s.State == bootDone {
			n++
		}
	}
	return n
}

func newBootModel() *bootModel { return &bootModel{Threshold: bootThreshold} }

// Start finishes whatever step was running and begins a new one.
func (m *bootModel) Start(label string, at time.Duration) {
	m.complete(at)
	m.Steps = append(m.Steps, bootStep{Label: label, State: bootRunning, startAt: at})
	if at > m.Elapsed {
		m.Elapsed = at
	}
}

// Detail annotates the running step with what it found ("41 found"). A no-op when nothing is
// running, so an out-of-order call can never invent a step.
func (m *bootModel) Detail(detail string) {
	if i := m.runningIdx(); i >= 0 {
		m.Steps[i].Detail = detail
	}
}

// Complete finishes the running step. Called by Start for the previous step, and once at the end.
func (m *bootModel) Complete(at time.Duration) {
	m.complete(at)
	if at > m.Elapsed {
		m.Elapsed = at
	}
}

func (m *bootModel) complete(at time.Duration) {
	i := m.runningIdx()
	if i < 0 {
		return
	}
	m.Steps[i].State = bootDone
	if d := at - m.Steps[i].startAt; d > 0 {
		m.Steps[i].Elapsed = d
	}
}

func (m *bootModel) runningIdx() int {
	for i := range m.Steps {
		if m.Steps[i].State == bootRunning {
			return i
		}
	}
	return -1
}

// bootElapsed formats a step's duration the way a human reads it: milliseconds while it is
// milliseconds, seconds once it is worth caring about.
func bootElapsed(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// renderBootFrame draws the splash: the wordmark with the gradient sweeping across it (the same
// mark and palette as loadingFrame — one boot aesthetic, not a second visual language) with the
// steps listed under it.
//
// Returns "" when boot has not yet run past the threshold, so a fast launch paints nothing at
// all. The returned frame never exceeds cols × rows: every line is clipped to cols and the step
// list is trimmed to what fits.
// renderBootFrame draws the launch splash: the wordmark, and a progress bar under it.
//
// IT USED TO LIST THE STEPS — "loading your theme", "finding your sessions · 41 found" — each with
// its own marker and elapsed time. That was removed on the owner's call. The information was real
// but nobody wanted it: a launcher that narrates its own startup makes the wait feel longer than
// the same wait behind a bar, and the step names meant nothing to the person reading them. The
// steps are still tracked, because the bar is built from them and because a test asserts the
// sequence; they are simply not printed.
func renderBootFrame(m bootModel, cols, rows int) string {
	if m.Elapsed < m.Threshold {
		return ""
	}
	if cols < 24 || rows < 6 {
		return "" // too small to say anything useful without mangling the screen
	}

	const word = "☎  P A R T Y L I N E"
	wordW := brand.VisWidth(word)

	barW := wordW
	if barW > cols-4 {
		barW = cols - 4
	}
	bar := renderBootBar(m, barW)

	wordCol := (cols - wordW) / 2
	if wordCol < 1 {
		wordCol = 1
	}
	barCol := (cols - barW) / 2
	if barCol < 1 {
		barCol = 1
	}
	top := (rows - 3) / 2
	if top < 1 {
		top = 1
	}

	var f strings.Builder
	f.WriteString("\x1b[2J")
	fmt.Fprintf(&f, "\x1b[%d;%dH%s", top, wordCol, brand.WordmarkPhase(m.Phase))
	fmt.Fprintf(&f, "\x1b[%d;%dH%s", top+2, barCol, bar)
	return f.String()
}

// renderBootBar is the progress indicator, width glyphs wide.
//
// Determinate once the launch knows how many steps it has: filled proportionally, in amber, with
// the remainder in the same dim grey the rest of the chrome uses.
//
// Indeterminate before that, and it SWEEPS rather than filling — a short block moving left to
// right on the gradient phase. A bar that fills from a made-up denominator is a lie that gets
// found out on the one launch where it matters, which is the slow one.
func renderBootBar(m bootModel, width int) string {
	if width < 4 {
		return ""
	}
	const (
		full  = "━"
		empty = "─"
	)
	amber := brand.Fg(brand.AmberRGB)
	const dim = "\x1b[38;5;240m"
	const reset = "\x1b[0m"

	if m.Total <= 0 {
		// Sweep: a run of `span` glyphs whose position advances with the tick.
		span := width / 6
		if span < 2 {
			span = 2
		}
		// Offset by span so the run is on screen at phase 0 — otherwise the first frames of an
		// indeterminate bar are blank, which reads as nothing happening at the exact moment the
		// splash appears.
		pos := m.Phase%(width+span) + span
		var b strings.Builder
		b.WriteString(dim)
		for i := 0; i < width; i++ {
			if i >= pos-span && i < pos {
				b.WriteString(reset + amber + full + reset + dim)
				continue
			}
			b.WriteString(empty)
		}
		b.WriteString(reset)
		return b.String()
	}

	done := m.doneCount()
	if done > m.Total {
		done = m.Total
	}
	filled := done * width / m.Total
	// Never paint a full bar until the work is actually finished — a bar that reads 100% while
	// the screen is still up is the specific thing that makes a launcher feel stuck.
	if filled >= width && done < m.Total {
		filled = width - 1
	}
	return amber + strings.Repeat(full, filled) + reset +
		dim + strings.Repeat(empty, width-filled) + reset
}

// ---- the live display ----

// bootReporter is what the boot doors report through. The live display implements it; a test
// injects a recorder instead and asserts the steps without needing a terminal.
type bootReporter interface {
	Step(label string)    // start a step (marks the previous one done)
	Detail(detail string) // annotate the running step with what it found
	Total(n int)          // how many steps to expect, for the bar; only ever grows
	Done()                // finish the running step and tear the display down
}

// bootRecorder is the test seam: it keeps the steps rather than painting them.
type bootRecorder struct {
	mu    sync.Mutex
	steps []bootStep
	model *bootModel
}

func newBootRecorder() *bootRecorder { return &bootRecorder{model: newBootModel()} }

func (r *bootRecorder) Step(label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.model.Start(label, r.model.Elapsed)
	r.steps = append(r.steps, bootStep{Label: label})
}

func (r *bootRecorder) Total(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.model.SetTotal(n)
}

func (r *bootRecorder) Detail(detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.model.Detail(detail)
	if n := len(r.steps); n > 0 {
		r.steps[n-1].Detail = detail
	}
}

func (r *bootRecorder) Done() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.model.Complete(r.model.Elapsed)
}

// Labels returns the step labels in the order they were reported.
func (r *bootRecorder) Labels() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.steps))
	for i, s := range r.steps {
		out[i] = s.Label
	}
	return out
}

// bootSplash is the live display: the model plus a ticker that paints it. Every method is safe
// from any goroutine — the ticker paints while the boot sequence reports.
type bootSplash struct {
	mu      sync.Mutex
	m       *bootModel
	started time.Time
	painted bool
	stop    chan struct{}
	done    chan struct{}
	out     *os.File
}

// newBootDisplay builds the reporter for one launch. A package var so a test can swap it for a
// recorder; production returns the live splash.
var newBootDisplay = func() bootReporter { return startBootSplash(os.Stdout) }

// startBootSplash begins timing the boot and starts the repaint ticker. When out is not a
// terminal it still tracks steps but paints nothing, so the headless paths (`ptln llms ls`, a
// piped `ptln`) are byte-for-byte unaffected.
func startBootSplash(out *os.File) *bootSplash {
	b := &bootSplash{m: newBootModel(), started: time.Now(), stop: make(chan struct{}), done: make(chan struct{}), out: out}
	if out == nil || !term.IsTerminal(int(out.Fd())) {
		close(b.done)
		return b
	}
	go b.loop()
	return b
}

func (b *bootSplash) loop() {
	defer close(b.done)
	t := time.NewTicker(bootTick)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			b.paint()
		}
	}
}

func (b *bootSplash) paint() {
	cols, rows, err := term.GetSize(int(b.out.Fd()))
	if err != nil {
		return
	}
	b.mu.Lock()
	b.m.Elapsed = time.Since(b.started)
	b.m.Phase++
	frame := renderBootFrame(*b.m, cols, rows)
	if frame == "" {
		b.mu.Unlock()
		return
	}
	if !b.painted {
		b.painted = true
		// The ALTERNATE screen, exactly as the mux itself does. The splash is ephemeral, and
		// whatever was in the terminal (including the held-fork warnings --resume prints just
		// before this) must survive it — clearing the primary screen would eat the user's
		// scrollback on every slow launch.
		frame = "\x1b[?1049h\x1b[?25l" + frame
	}
	b.mu.Unlock()
	_, _ = b.out.WriteString(frame)
}

func (b *bootSplash) Step(label string) {
	b.mu.Lock()
	b.m.Start(label, time.Since(b.started))
	b.mu.Unlock()
}

func (b *bootSplash) Detail(detail string) {
	b.mu.Lock()
	b.m.Detail(detail)
	b.mu.Unlock()
}

// Total tells the bar how many steps to expect. Before the first call the bar sweeps.
func (b *bootSplash) Total(n int) {
	b.mu.Lock()
	b.m.SetTotal(n)
	b.mu.Unlock()
}

// Done stops the ticker and hands the screen back. Nothing is held open for effect: if the whole
// boot finished under the threshold the terminal was never touched, and nothing is cleared either.
func (b *bootSplash) Done() {
	b.mu.Lock()
	b.m.Complete(time.Since(b.started))
	b.mu.Unlock()
	select {
	case <-b.stop: // already stopped — Done is idempotent
	default:
		close(b.stop)
	}
	<-b.done
	b.mu.Lock()
	painted := b.painted
	b.mu.Unlock()
	if painted {
		_, _ = b.out.WriteString("\x1b[?1049l\x1b[?25h") // back to the primary screen, untouched
	}
}

// ---- wiring the doors ----

// bootReportRestores makes each session the mux is about to reopen its own step — "reopening
// cyberpunk-game (3 of 7)". This is the wait that actually hurts: `ptln --resume` costs one
// engine start per saved session, and until now it showed nothing throughout.
//
// It hooks ptymux.SpawnProgress, which fires just before each initial spec is spawned, IN THE
// ORDER New already spawns them — the display is bolted onto the existing sequence, never
// driving it. Returns the teardown, which must be deferred by the caller.
func bootReportRestores(rep bootReporter, specs []ptymux.Spec) func() {
	if rep == nil || len(specs) == 0 {
		return func() {}
	}
	n := len(specs)
	rep.Total(4 + n) // the launch steps plus one per session being reopened
	ptymux.SpawnProgress = func(sp ptymux.Spec, i, _ int) {
		label := strings.TrimSpace(sp.Label)
		if label == "" {
			label = "a session"
		}
		if n == 1 {
			rep.Step("reopening " + label)
			return
		}
		rep.Step(fmt.Sprintf("reopening %s (%d of %d)", label, i+1, n))
	}
	return func() { ptymux.SpawnProgress = nil }
}

// llmsBoot runs the switchboard's startup sequence, reporting each step as it completes.
//
// This is exactly the sequence runLLMSApp always ran — loadTheme → collectSessions →
// loadLLMMeta → firstLauncherRun, in that order, one after the other. Nothing here is
// reordered, parallelised or skipped; extracting it just gives the steps somewhere to be
// reported from, and gives a test somewhere to inject a recorder that needs no terminal.
//
// The wording is what the user is waiting for, not what we call it: "finding your sessions"
// rather than collectSessions.
func llmsBoot(rep bootReporter) (all []aiSession, meta map[string]sessMeta, firstRun bool) {
	rep.Total(4) // the four steps below; restores add themselves once the workspace is read
	rep.Step("loading your theme")
	loadTheme() // restore the user's chosen colour theme (persisted)

	rep.Step("finding your sessions")
	all = collectSessions()
	rep.Detail(fmt.Sprintf("%d found", len(all)))

	rep.Step("reading history")
	meta = loadLLMMeta()

	rep.Step("preparing the launcher")
	firstRun = firstLauncherRun()
	return all, meta, firstRun
}
