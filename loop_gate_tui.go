package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"
)

// `ptln loop gate`: the approval popup (UI plan step 2). It opens from the ctrl-\ menu's `a` row,
// shows what is waiting for the person with a summary first, and takes their decision with one
// key. Bubble Tea v2, styled from internal/theme.

type gateMode int

const (
	gateSummary gateMode = iota
	gateDetails
	gateNote
	gateConfirm // a decision that lets work proceed waits for the code shown on screen
)

// gateActions are what the popup does; tests replace them.
type gateActions struct {
	list    func() []*loopRun
	approve func(*loopRun) error
	reject  func(*loopRun, string) error
	decide  func(r *loopRun, finding, decision string) error
	stop    func(*loopRun) error
	reverse func(r *loopRun, which string) (string, error) // reverse the arbiter's dismiss which (S<step>-<id>) in r's batch
	allowed func() error                                   // refuses a decision that did not come from a person at a popup
	tab     func(*loopRun) string
	code    func() string // the confirmation code; random in the popup, fixed in tests
}

// gateInfo is what the summary shows for one run, read once when the list loads rather than on
// every frame (View runs after each key).
type gateInfo struct {
	goal, tab, revision string
	dispute             loopTracked
}

type gateModel struct {
	remoteQR      string
	remoteAssets  string
	remoteWait    tea.Cmd
	remotePending *loopRun
	act           gateActions
	runs          []*loopRun
	info          map[string]gateInfo
	scroll        int // first summary line shown when the summary is taller than the popup
	sel           int
	mode          gateMode
	note          textinput.Model
	// The confirmation: approving or dismissing a finding lets work go ahead, so it needs the
	// code shown in the popup typed back. It addresses one threat: keys injected into a popup the
	// injector cannot see (tmux send-keys -K), since a popup's contents cannot be read with
	// capture-pane. It does not stop a process that runs this screen on a terminal it owns and
	// reads; see gateAllowed.
	code    string
	typed   string
	pending func() error
	what    string
	label   string // what the code confirms, e.g. "approve loop ab12cd"
	vp      viewport.Model
	w, h    int
	result  string // what the last key did, or why it was refused
	// list: the first screen, one row per item. Every key acts on the selected item there as on
	// its own screen; ⏎ opens the item, esc returns to the list.
	list bool
}

func newGateModel(act gateActions) gateModel {
	ti := textinput.New()
	ti.Placeholder = "what should change in the brief"
	m := gateModel{act: act, note: ti, vp: viewport.New(), w: 90, h: 30, list: true}
	return m.reload()
}

// reloadKeepingSelection reloads and keeps the same run selected, and the screen where it was
// scrolled to, when it is still listed.
func (m gateModel) reloadKeepingSelection() gateModel {
	var id string
	if r := m.current(); r != nil {
		id = r.ID
	}
	result, scroll := m.result, m.scroll
	m = m.reload()
	m.result = result
	for i, r := range m.runs {
		if r.ID == id {
			m.sel, m.scroll = i, min(scroll, m.maxScroll())
		}
	}
	if m.list {
		m.scroll = m.listScroll()
	}
	return m
}

// reload lists what is waiting now and reads each run's summary details once.
func (m gateModel) reload() gateModel {
	misses := map[string][]time.Time{}
	for _, r := range m.runs {
		if r.Remote != nil {
			misses[r.ID] = r.Remote.Misses
		}
	}
	m.runs = m.act.list()
	if m.remotePending != nil {
		m.runs = append([]*loopRun{m.remotePending}, m.runs...)
	}
	m.info = map[string]gateInfo{}
	for _, r := range m.runs {
		if r.Remote != nil {
			if at, ok := misses[r.ID]; ok {
				r.Remote.Misses = at
			}
			continue
		}
		in := gateInfo{goal: briefGoal(latestBrief(r)), tab: m.act.tab(r), revision: briefRevisionNote(r)}
		if batchPending(r) {
			// A batch's goal is every step's, one per line.
			in.goal = strings.TrimSpace(mdSections(latestBrief(r))["goal"])
		}
		readLoopJSON(r.ID, "dispute.json", &in.dispute)
		m.info[r.ID] = in
	}
	m.sel = min(m.sel, max(len(m.runs)-1, 0))
	m.scroll = 0
	return m
}

// gateTick refreshes the list while the popup is open, so running work shows its progress.
type gateTick struct{}

const gateRefresh = 2 * time.Second

func gateTickCmd() tea.Cmd {
	return tea.Tick(gateRefresh, func(time.Time) tea.Msg { return gateTick{} })
}

func (m gateModel) Init() tea.Cmd { return tea.Batch(gateTickCmd(), m.remoteWait) }

func (m gateModel) current() *loopRun {
	if m.sel < 0 || m.sel >= len(m.runs) {
		return nil
	}
	return m.runs[m.sel]
}

func (m gateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case remotePairRequest:
		m.remotePending = phoneGateRun(msg.Item)
		m.remoteQR = ""
		m = m.reload()
		m.sel = 0
		m.list = false
		return m, nil
	case gateTick:
		if m.remotePending != nil && !m.remotePending.Remote.Expires.IsZero() && time.Now().After(m.remotePending.Remote.Expires) {
			m.remotePending.Remote.Refuse()
			m.remotePending = nil
			m.mode = gateSummary
			m.pending = nil
			m = m.reload()
			m.result = "Pairing timed out; nothing saved"
		}
		// Only on the summary: a refresh must not move what the person is typing a code or a
		// note against, or reading in the full brief.
		if m.mode == gateSummary {
			m = m.reloadKeepingSelection()
		}
		return m, gateTickCmd()
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.vp.SetWidth(m.w - 4)
		// The frame around the details: 2 padding rows, a 2-row heading, a blank row and the keys row.
		m.vp.SetHeight(max(m.h-7, 3))
		return m, nil
	case tea.KeyPressMsg:
		switch m.mode {
		case gateNote:
			return m.updateNote(msg)
		case gateConfirm:
			return m.updateConfirm(msg)
		case gateDetails:
			switch msg.String() {
			case "esc", "q", "v":
				m.mode = gateSummary
				return m, nil
			}
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		return m.updateSummary(msg)
	}
	return m, nil
}

func (m gateModel) updateSummary(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.remoteQR != "" {
		switch k.String() {
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	r := m.current()
	if r != nil && r.Remote != nil {
		item := r.Remote
		switch k.String() {
		case "a":
			label := "Revoke phone "
			if item.Pair {
				label = "Pair phone "
			}
			label += termSafe(item.Phone.Name) + ", fingerprint " + item.Phone.Fingerprint
			m = m.confirm(label, "confirmed", item.Decide)
			return m, nil
		case "r":
			if item.Pair {
				item.Refuse()
				m.remotePending = nil
				m = m.reload()
				return m, nil
			}
			return m, nil
		case "v", "s", "x", "f", "d":
			return m, nil
		}
	}
	switch k.String() {
	case "esc":
		if !m.list {
			m.list, m.scroll = true, 0
			return m, nil
		}
		if m.remoteWait == nil {
			gateSnooze(m.runs)
		} // what was shown: an item that arrived since must still open the popup
		return m, tea.Quit
	case "q", "ctrl+c":
		if m.remoteWait == nil {
			gateSnooze(m.runs)
		}
		return m, tea.Quit
	case "enter":
		if m.list && r != nil {
			m.list, m.result = false, ""
		}
	case "up", "k":
		m.sel = max(m.sel-1, 0)
		m.scroll = m.selectionScroll()
	case "down", "j":
		m.sel = min(m.sel+1, max(len(m.runs)-1, 0))
		m.scroll = m.selectionScroll()
	case "pgdown", "space":
		m.scroll = min(m.scroll+1, m.maxScroll())
	case "pgup":
		m.scroll = max(m.scroll-1, 0)
	case "v":
		if r != nil {
			// The brief is the session's text: each line goes through termSafe before rendering.
			lines := strings.Split(latestBrief(r), "\n")
			for i, l := range lines {
				lines[i] = termSafe(l)
			}
			m.vp.SetContent(renderMarkdown(strings.Join(lines, "\n"), "", max(m.w-6, 20)))
			m.vp.GotoTop()
			m.mode = gateDetails
		}
	case "a":
		if r != nil && r.State == loopAwaiting {
			m = m.confirm("approve loop "+termSafe(r.ID), "approved", func() error { return m.act.approve(r) })
		} else if r != nil && r.State == loopAwaitYou {
			// A dispute is ruled, not approved: say so rather than ignore the key.
			m.result = "this is a disputed finding: d dismisses it, f has it fixed"
		}
	case "x":
		// Overruling the arbiter is a decision like any other: it takes the code.
		if r != nil && m.act.reverse != nil {
			if a := gateStanding(r); len(a) > 0 {
				id := fmt.Sprintf("S%d-%s", a[0].Step, a[0].Finding.ID)
				m = m.confirm("reverse the arbiter's dismiss of "+termSafe(id), "reversed: it waits for you with the batch's other disputes", func() error {
					_, err := m.act.reverse(r, id)
					return err
				})
			}
		}
	case "s":
		// Stopping ends the run, and its batch: confirmed with the code like any decision.
		if r != nil {
			m = m.confirm("stop loop "+termSafe(r.ID), "stopped", func() error { return m.act.stop(r) })
		}
	case "r":
		if r != nil && r.State == loopAwaiting {
			m.mode, m.result = gateNote, ""
			m.note.SetValue("")
			return m, m.note.Focus()
		}
	case "f":
		// Fix takes the code too: either ruling closes the dispute, and a decided finding is not
		// raised in later reviews, so a forced "fix" would end the P1 as surely as a dismiss.
		if r != nil && r.State == loopAwaitYou {
			finding := m.info[r.ID].dispute.ID
			m = m.confirm("fix "+termSafe(finding)+" on loop "+termSafe(r.ID), "fix", func() error { return m.act.decide(r, finding, "fix") })
		}
	case "d":
		if r != nil && r.State == loopAwaitYou {
			finding := m.info[r.ID].dispute.ID
			m = m.confirm("dismiss "+termSafe(finding)+" on loop "+termSafe(r.ID), "dismissed", func() error { return m.act.decide(r, finding, "dismiss") })
		}
	}
	return m, nil
}

func (m gateModel) updateNote(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = gateSummary
		return m, nil
	case "enter":
		r := m.current()
		note := strings.TrimSpace(m.note.Value())
		m.mode = gateSummary
		if r == nil {
			return m, nil
		}
		if note == "" {
			m.mode = gateNote
			m.result = "a note is required so the session knows what to change; esc cancels"
			return m, nil
		}
		// Rejecting takes the code too: in a popup that opened by itself, a stray r, text and ⏎
		// must not reject a brief.
		m = m.confirm("reject loop "+termSafe(r.ID), "rejected", func() error { return m.act.reject(r, note) })
		return m, nil
	}
	var cmd tea.Cmd
	m.note, cmd = m.note.Update(k)
	return m, cmd
}

// confirm asks for the code before running do, unless wrong codes have locked the run.
func (m gateModel) confirm(label, what string, do func() error) gateModel {
	if r := m.current(); r != nil {
		if d := gateRunLockedFor(r); d > 0 {
			if r.Remote != nil {
				m.result = fmt.Sprintf("too many wrong codes; try again in %s", d)
				return m
			}
			m.result = fmt.Sprintf("too many wrong codes for this loop; try again in %s. If they were not yours, something else is typing into this popup; if they were, deleting %s clears the count", d, loopFile(r.ID, "gate-misses.json"))
			return m
		}
	}
	m.mode, m.code, m.typed, m.pending, m.what, m.result = gateConfirm, m.act.code(), "", do, what, ""
	m.label = label
	return m
}

func (m gateModel) updateConfirm(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	switch {
	case s == "esc":
		m.mode, m.result = gateSummary, "cancelled; nothing was decided"
	case len(s) == 1 && s[0] >= '0' && s[0] <= '9':
		m.typed += s
		if len(m.typed) == len(m.code) {
			r, do, what := m.current(), m.pending, m.what
			m.mode = gateSummary
			if m.typed != m.code || r == nil {
				if r != nil {
					gateRunMiss(r)
				}
				m.result = "the code did not match; nothing was decided"
				return m, nil
			}
			gateConfirmMethod = "code"
			m = m.decide(r, what, do)
		}
	}
	return m, nil
}

// gateCode is a random four-digit confirmation code, uniform over 0000-9999.
func gateCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "0000"
	}
	return fmt.Sprintf("%04d", n.Int64())
}

// Wrong codes are counted per run on disk, so reopening the popup does not reset them: after
// gateMaxMisses within gateMissWindow, the run takes no confirmations until the window passes. This
// bounds blind key injection, not a process that can also delete the file: anything running as the
// person can, so the bound is a speed bump, not a guarantee.
const (
	gateMaxMisses  = 3
	gateMissWindow = 10 * time.Minute
)

func gateMisses(id string) []time.Time {
	var at []time.Time
	readLoopJSON(id, "gate-misses.json", &at)
	cutoff := time.Now().Add(-gateMissWindow)
	kept := at[:0]
	for _, t := range at {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// gateLockedFor is how long confirmations stay refused for the run; zero when they are not.
func gateLockedFor(id string) time.Duration {
	at := gateMisses(id)
	if len(at) < gateMaxMisses {
		return 0
	}
	return time.Until(at[len(at)-gateMaxMisses].Add(gateMissWindow)).Round(time.Second)
}

func gateMiss(id string) {
	writeLoopJSON(id, "gate-misses.json", append(gateMisses(id), time.Now()))
}

// Pending phone confirmations keep their failed attempts in memory too.
func gateRunMiss(r *loopRun) {
	if r.Remote == nil {
		gateMiss(r.ID)
		return
	}
	r.Remote.Misses = append(r.Remote.Misses, time.Now())
}
func gateRunLockedFor(r *loopRun) time.Duration {
	if r.Remote == nil {
		return gateLockedFor(r.ID)
	}
	var kept []time.Time
	for _, at := range r.Remote.Misses {
		if time.Since(at) < gateMissWindow {
			kept = append(kept, at)
		}
	}
	r.Remote.Misses = kept
	if len(kept) < gateMaxMisses {
		return 0
	}
	return time.Until(kept[len(kept)-gateMaxMisses].Add(gateMissWindow)).Round(time.Second)
}

// decide checks the gate, acts, and reloads what is still waiting.
func (m gateModel) decide(r *loopRun, what string, do func() error) gateModel {
	// The confirmation method describes this decision only, recorded or not.
	defer func() { gateConfirmMethod = "none" }()
	if err := m.act.allowed(); err != nil {
		m.result = "refused: " + err.Error()
		return m
	}
	if err := do(); err != nil {
		m.result = "failed: " + err.Error()
		return m
	}
	// A decision the person confirmed clears their earlier typos.
	if r.Remote == nil {
		_ = os.Remove(loopFile(r.ID, "gate-misses.json"))
	} else {
		r.Remote.Misses = nil
	}
	if r.Remote != nil && r.Remote.Pair {
		m.remotePending = nil
	}
	m = m.reload()
	m.result = what
	if r.Remote == nil {
		m.result = fmt.Sprintf("%s loop %s", what, termSafe(r.ID))
	}
	return m
}

// gatePanes is every pane's tty and process on the server, the contractor session's included:
// contractor panes are agents, which is what gateAllowed must refuse.
func gatePanes() ([]string, map[int]bool, error) {
	out, err := tmuxCmd("list-panes", "-a", "-F", "#{pane_tty} #{pane_pid}").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot list panes to check where this runs: %v", err)
	}
	var ttys []string
	pids := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			ttys = append(ttys, f[0])
			if pid, err := strconv.Atoi(f[1]); err == nil {
				pids[pid] = true
			}
		}
	}
	return ttys, pids, nil
}

// gateAllowed refuses a decision from the places an agent can reach without the person:
//   - below a claude or codex process (a command the agent runs);
//   - on any pane's terminal (the agent can type into a pane with send-keys);
//   - inside any pane's process tree, such as `script ptln loop gate` started in a new window, which
//     has its own terminal but still descends from that pane's process.
//
// A popup started by tmux descends from the tmux server, not from a pane, and passes all three.
//   - not opened as a tmux popup. A popup runs its command directly under the tmux server, as its own
//     session leader on a terminal tmux made (seen in a real decision's provenance: the gate was
//     pid 21461, session 21461, parent 20193 = the tmux server). A process an agent starts on a
//     terminal it owns has another parent (script, init); run-shell has no terminal; a new window
//     is a pane, refused above.
//   - more than one tmux client attached. A popup opened on a client the agent attached itself, on a
//     terminal it reads, would show the agent the confirmation code. One client means the popup is
//     on the person's screen.
//
// A popup the agent opens on the person's own client passes these, but the agent cannot read the
// confirmation code there. Nothing local is proof against a process running as the person; each
// decision also records its terminal and process chain (gateProvenance) so a forged one is findable.
func gateAllowed() error {
	if pid, name := engineAncestor(); pid > 0 {
		return fmt.Errorf("running below %s (pid %d)", name, pid)
	}
	in, err := os.Stdin.Stat()
	if err != nil {
		return err
	}
	st, ok := in.Sys().(*syscall.Stat_t)
	if !ok || in.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("not at a terminal")
	}
	ttys, pids, err := gatePanes()
	if err != nil {
		return err
	}
	if err := gateNotAPane(uint64(st.Rdev), ttys); err != nil {
		return err
	}
	if err := gateNotUnderAPane(processAncestors(os.Getpid()), pids); err != nil {
		return err
	}
	srv, err := tmuxCmd("display-message", "-p", "#{pid}").Output()
	if err != nil {
		return fmt.Errorf("cannot ask tmux for its server: %v", err)
	}
	server, _ := strconv.Atoi(strings.TrimSpace(string(srv)))
	sid, _ := unix.Getsid(0)
	clients, err := tmuxCmd("list-clients", "-F", "#{client_pid} #{client_tty}").Output()
	if err != nil {
		return fmt.Errorf("cannot list tmux clients: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(clients)), "\n")
	if strings.TrimSpace(string(clients)) == "" {
		lines = nil
	}
	if err := gateInPopup(os.Getpid(), sid, os.Getppid(), server, lines); err != nil {
		return err
	}
	// Whose client it is: a client an agent attached runs below the agent, or inside a pane.
	f := strings.Fields(lines[0])
	if len(f) != 2 {
		return fmt.Errorf("cannot read the attached tmux client (%q)", lines[0])
	}
	{
		cpid, _ := strconv.Atoi(f[0])
		chain := append([]int{cpid}, processAncestors(cpid)...)
		if name := engineInChain(chain); name != "" {
			return fmt.Errorf("the attached tmux client runs below %s, not in your terminal", name)
		}
		if err := gateNotUnderAPane(chain, pids); err != nil {
			return fmt.Errorf("the attached tmux client runs inside a session pane, not in your terminal")
		}
		dev := clientDev(f[1])
		if dev == 0 {
			return fmt.Errorf("cannot read the attached tmux client's terminal %q", f[1])
		}
		if err := gateNotAPane(dev, ttys); err != nil {
			return fmt.Errorf("the attached tmux client is on a session pane's terminal")
		}
	}
	return nil
}

// engineInChain names a claude or codex process among pids, or "".
func engineInChain(pids []int) string {
	for _, p := range pids {
		out, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(p)).Output()
		if err != nil {
			continue
		}
		if name := engineProgram(strings.Fields(string(out))); name != "" {
			return name
		}
	}
	return ""
}

// clientDev is the device number of a client's terminal, or 0.
func clientDev(tty string) uint64 {
	fi, err := os.Stat(tty)
	if err != nil {
		return 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Rdev)
	}
	return 0
}

// gateInPopup refuses unless this process has a tmux popup's signature (its own session leader,
// started directly by the tmux server) and the person's client is the only one attached.
func gateInPopup(pid, sid, ppid, server int, clients []string) error {
	if server <= 0 || ppid != server || sid != pid {
		return fmt.Errorf("not opened as a tmux popup (parent %d, session %d); open it with ctrl-\\ then a", ppid, sid)
	}
	if len(clients) != 1 {
		return fmt.Errorf("%d tmux clients are attached (%s); a decision needs exactly one: detach the others, then open the popup with ctrl-\\ then a", len(clients), strings.Join(clients, ", "))
	}
	return nil
}

// gateNotUnderAPane refuses when any ancestor is a pane's process.
func gateNotUnderAPane(ancestors []int, panePIDs map[int]bool) error {
	for _, pid := range ancestors {
		if panePIDs[pid] {
			return fmt.Errorf("running inside a session pane's processes (pid %d), not the approval popup", pid)
		}
	}
	return nil
}

// processAncestors lists pid's ancestors, nearest first, up to init.
func processAncestors(pid int) []int {
	var out []int
	for i := 0; i < 64 && pid > 1; i++ {
		b, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			break
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 1 {
			break
		}
		out = append(out, pid)
	}
	return out
}

// gateNotAPane refuses when dev is the device of any of the pane terminals.
func gateNotAPane(dev uint64, paneTTYs []string) error {
	for _, p := range paneTTYs {
		if fi, err := os.Stat(p); err == nil {
			if ps, ok := fi.Sys().(*syscall.Stat_t); ok && uint64(ps.Rdev) == dev {
				return fmt.Errorf("this is a session pane (%s), not the approval popup", p)
			}
		}
	}
	return nil
}

// loopGate is `ptln loop gate`, run inside the popup.
func loopGate() {
	act := gateActions{list: gateRuns,
		approve: func(r *loopRun) error { return loopApproveRun(r.ID, r.Revision) },
		reject:  func(r *loopRun, note string) error { return loopRejectRun(r.ID, r.Revision, note) },
		decide:  func(r *loopRun, finding, d string) error { return loopDecideDispute(r.ID, finding, d) },
		stop:    func(r *loopRun) error { return loopStopRun(r.ID) },
		reverse: func(r *loopRun, which string) (string, error) { return batchReverse(r.Batch, which) },
		allowed: gateAllowed, tab: loopTabFor, code: gateCode}
	defer markGateOpen()() // the watcher does not open a second popup while this one runs
	m := newGateModel(act)
	if i := indexOfArg(os.Args, "--run"); i >= 0 && i+1 < len(os.Args) {
		m = m.selectRun(os.Args[i+1])
	}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fatal(err)
	}
}

// selectRun opens the board's requested run when it is still eligible for the gate.
func (m gateModel) selectRun(id string) gateModel {
	for n, r := range m.runs {
		if r.ID == id {
			m.sel, m.list = n, false
			break
		}
	}
	return m
}

// loopTabFor names the tab the run's session is in, for the summary.
func loopTabFor(r *loopRun) string {
	for _, k := range r.Session {
		if pane, ok := strings.CutPrefix(k, "pane:"); ok {
			if out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_index} #{window_name}").Output(); err == nil {
				return "tab " + strings.TrimSpace(string(out))
			}
		}
	}
	return "its session"
}

// gateRuns is what the popup lists: everything waiting for the person, then active runs, which
// can only be stopped here.
func gateRuns() []*loopRun {
	out := append(savedPhoneGateRuns(), loopWaitingRuns()...)
	runs := loopRuns()
	for i := len(runs) - 1; i >= 0; i-- {
		if r := runs[i]; r.live() && !loopWaiting(r) {
			out = append(out, r)
		}
	}
	// A finished batch whose arbiter dismissed findings stays here while its branch exists (it is
	// deleted when its PR merges), so the person can still reverse them: its latest run stands for it.
	listed := map[string]bool{}
	for _, r := range out {
		listed[r.Batch] = true
	}
	for _, r := range runs {
		if r.Batch == "" || listed[r.Batch] {
			continue
		}
		listed[r.Batch] = true
		if len(gateStanding(r)) > 0 && batchBranchExists(r) {
			out = append(out, r)
		}
	}
	return out
}

// batchBranchExists is whether r's batch's branch is still in its repository.
func batchBranchExists(r *loopRun) bool {
	b, err := loadBatch(r.Batch)
	if err != nil || b.Branch == "" {
		return false
	}
	_, err = gitLoop(r.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+b.Branch)
	return err == nil
}

// gateStanding is the arbiter dismissals in r's batch the person can still reverse.
func gateStanding(r *loopRun) []loopArbitration {
	if r.Batch == "" {
		return nil
	}
	b, err := loadBatch(r.Batch)
	if err != nil {
		return nil
	}
	return standingDismissals(b)
}

// gateConfirmMethod is how the next decision was confirmed, for its provenance: code;
// none only for a decision made outside the popup's confirmation (the terminal commands are gone).
var gateConfirmMethod = "none"
