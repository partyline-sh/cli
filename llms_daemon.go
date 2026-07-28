// S2 — Availability in the session manager. Folds the remote-launch daemon's project
// registry + presence into `ptln llms`, so a human marks a project "joinable to parties"
// with one key (the [P] cycle) and flips Online/Offline without ever typing
// `daemon add-project` / `daemon run`.
//
// Two pieces live here:
//   - the registry glue: load/cycle the LOCAL daemon registry keyed by directory, so the
//     tree can show + toggle which projects are advertised (label = basename, preset = spec).
//   - daemonPresence: the "manager-open = daemon" controller (model A). While Online it
//     holds the device's outbound api.DaemonStream open (so the control plane sees this
//     machine present + advertising). It NEVER writes to the screen — the mux is the only
//     writer — so the goroutine only mutates in-memory state the TUI reads on its next paint.
//
// What S2 deliberately does NOT do: act on incoming launch requests. onLaunch only bumps a
// pending counter for the status line; the approve modal + spawn-on-accepted is S3.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/api"
)

// ---- registry glue (directory-keyed view of the daemon registry) ------------

// loadJoinable returns the registered (advertised) projects keyed by absolute path, so the
// tree can answer "is THIS project's dir joinable?" in O(1) while rendering.
func loadJoinable() map[string]daemonProject {
	out := map[string]daemonProject{}
	for _, p := range loadDaemonRegistry().Projects {
		out[p.Path] = p
	}
	return out
}

// cycleJoinable advances a project's availability one step on each press of [P]:
//
//	not-joinable → joinable(ask) → joinable(auto) → not-joinable
//
// It mutates the LOCAL registry (label = basename, preset = spec — the locked default) and
// returns the new state ("off"|"ask"|"auto") + a user-facing flash. Turning a project ON
// fails (state "off", error flash) if another dir already claims that basename label — the
// registry keys by label, so a silent clobber would point the label at the wrong path.
func cycleJoinable(cwd string) (state, flash string) {
	if strings.TrimSpace(cwd) == "" {
		return "off", "no directory recorded — can't advertise this project"
	}
	reg := loadDaemonRegistry()
	label := filepath.Base(cwd)

	// Find the entry for this exact dir, if any.
	idx := -1
	for i := range reg.Projects {
		if reg.Projects[i].Path == cwd {
			idx = i
			break
		}
	}

	if idx < 0 { // off → ask: register it, but guard the basename label first.
		for i := range reg.Projects {
			if reg.Projects[i].Label == label {
				return "off", "✗ label \"" + label + "\" already used by " + reg.Projects[i].Path
			}
		}
		reg.Projects = append(reg.Projects, daemonProject{Label: label, Path: cwd, Preset: "spec", Policy: "ask"})
		if err := saveDaemonRegistry(reg); err != nil {
			return "off", "✗ " + err.Error()
		}
		return "ask", "✓ joinable · Ask (approve each launch)"
	}

	switch reg.Projects[idx].launchPolicy() {
	case "ask": // ask → auto
		reg.Projects[idx].Policy = "auto"
		if err := saveDaemonRegistry(reg); err != nil {
			return "ask", "✗ " + err.Error()
		}
		return "auto", "✓ joinable · Auto (instant launch + FYI)"
	default: // auto → off: unregister
		reg.Projects = append(reg.Projects[:idx], reg.Projects[idx+1:]...)
		if err := saveDaemonRegistry(reg); err != nil {
			return "auto", "✗ " + err.Error()
		}
		return "off", "✓ not joinable"
	}
}

// ---- presence controller (manager-open = daemon, model A) -------------------

type presenceMode int

const (
	presOffline    presenceMode = iota // not advertising; the web picker greys this machine out (S4)
	presConnecting                     // enrolling / dialing the control plane
	presOnline                         // stream is up; the control plane sees us present
	presError                          // login needed, token revoked, or a hard failure
)

// pendingApproval is a launch request awaiting the owner's confirm in the manager (S3). The
// join ref is NOT here — it arrives with the `accepted` event and executeAccepted uses it.
type pendingApproval struct {
	reqID  string
	label  string
	preset string
	party  string
}

// daemonPresence holds the outbound daemon stream open while the manager is Online. All
// fields are guarded by mu; the stream goroutine only mutates state here (never the screen)
// and calls wake() to ask the mux to repaint, and render() reads a snapshot. cancel tears the
// goroutine down on Offline / app exit.
type daemonPresence struct {
	mu          sync.Mutex
	mode        presenceMode
	advertising int               // joinable projects mirrored this session
	pendingReqs []pendingApproval // launch requests awaiting this owner's approve (Ask projects)
	note        string            // error/status detail for the footer
	cancel      context.CancelFunc
	daemonID    string
	wake        func() // ask the mux to repaint (set after the mux is built); nil = no-op
}

func (p *daemonPresence) snapshot() (mode presenceMode, advertising, pending int, note, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode, p.advertising, len(p.pendingReqs), p.note, p.daemonID
}

// pendingList returns a copy of the launch requests awaiting approval (for the TUI banner).
func (p *daemonPresence) pendingList() []pendingApproval {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pendingApproval(nil), p.pendingReqs...)
}

// repaint asks the hosting mux to redraw — the bridge that lets an async stream event surface
// a banner without the goroutine touching the screen itself (the mux owns all output).
func (p *daemonPresence) repaint() {
	p.mu.Lock()
	w := p.wake
	p.mu.Unlock()
	if w != nil {
		w()
	}
}

func (p *daemonPresence) online() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode == presOnline || p.mode == presConnecting
}

func (p *daemonPresence) set(mode presenceMode, note string) {
	p.mu.Lock()
	p.mode, p.note = mode, note
	p.mu.Unlock()
}

// toggle flips Online/Offline. It returns a flash for the menu. Going Online needs a login
// (the device-enrolment call is owner-authenticated); the actual enroll + dial happen in the
// background goroutine so a slow network never freezes the TUI.
func (p *daemonPresence) toggle() string {
	if p.online() {
		p.goOffline()
		return "✓ daemon: offline"
	}
	if api.LoadToken() == "" {
		p.set(presError, "sign in first: ptln login")
		return "sign in to go online — run: ptln login"
	}
	p.goOnline()
	return "✓ daemon: connecting…"
}

func (p *daemonPresence) goOffline() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.mode, p.note, p.pendingReqs = presOffline, "", nil
	p.mu.Unlock()
}

// goOnline starts the presence goroutine: enroll the device if needed, mirror the joinable
// projects, then hold api.DaemonStream open with reconnect/backoff until cancelled.
func (p *daemonPresence) goOnline() {
	if serviceInstalled() {
		// Always-on (the OS service) already holds the stream — never run two consumers, or
		// both would act on `accepted` events. Reflect always-on instead of opening our own.
		p.set(presOffline, "always-on service is handling presence")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	if p.cancel != nil { // already running — shouldn't happen (online() gate), but be safe
		p.cancel()
	}
	p.cancel = cancel
	p.mode, p.note, p.advertising = presConnecting, "", len(loadJoinable())
	p.mu.Unlock()
	go p.run(ctx)
}

func (p *daemonPresence) run(ctx context.Context) {
	// Enroll this machine as a daemon device if it isn't already (reuses the persisted
	// device token across sessions). RegisterDaemon is owner-authenticated (login token).
	d := loadDaemonDevice()
	if d.Token == "" {
		label := "device"
		if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
			label = strings.TrimSpace(h)
		}
		id, token, err := api.New().RegisterDaemon(label)
		if err != nil {
			if isAuthErr(err) {
				p.set(presError, "sign in first: ptln login")
			} else {
				p.set(presError, "enroll failed: "+err.Error())
			}
			return
		}
		d = daemonDevice{DaemonID: id, Token: token, Base: api.Base()}
		if err := saveDaemonDevice(d); err != nil {
			p.set(presError, "save device: "+err.Error())
			return
		}
	}
	p.mu.Lock()
	p.daemonID = d.DaemonID
	p.mu.Unlock()

	// Advertise the joinable projects (best-effort; the local registry is the source of truth).
	p.remirror(d)

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := api.DaemonStream(ctx, d.Base, d.Token,
			func() { backoff = time.Second; p.set(presOnline, ""); p.repaint() },
			func(ev api.LaunchEvent) { // pending — auto-accept an Auto project, else queue a banner
				if isAutoProject(ev.ProjectLabel) {
					go func() { _ = daemonAuthorize(d, ev.ProjectLabel, ev.RequestID) }()
					return
				}
				p.mu.Lock()
				for _, q := range p.pendingReqs {
					if q.reqID == ev.RequestID { // already queued (reconnect re-push)
						p.mu.Unlock()
						return
					}
				}
				p.pendingReqs = append(p.pendingReqs, pendingApproval{reqID: ev.RequestID, label: ev.ProjectLabel, preset: ev.Preset, party: ev.PartyID})
				p.mu.Unlock()
				p.repaint() // surface the approve banner in real time
			},
			func(ev api.LaunchEvent) { // accepted (by web or CLI) — execute, then drop the banner
				go func() {
					_, _ = executeAccepted(d, ev)
					p.dropPending(ev.RequestID)
					p.repaint()
				}()
			},
			func(ev api.RunEvent) { // O.2 run-profile — the TUI manager only auto-runs Auto
				// projects; interactive run-approval (ask) is via the `ptln daemon run` console
				// in O.2 (the manager has no run-approval banner yet — a later slice).
				if isAutoProject(ev.ProjectLabel) {
					go func() { _ = executeRun(d, ev) }()
				}
			},
			func(string) {}, // web-initiated kill: handled by `daemon run`, not the manager
			func(string) {}, // web-initiated RUN kill: same — the always-on service owns the crank child
			func(string) {}, // web-initiated RUN pause: same — the always-on service owns the crank child
			func(string) {}, // web-initiated RUN resume: same — the always-on service owns the crank child
			nil,             // web restart: only the always-on `daemon run` service acts on it
			nil,             // web relabel: renamed by the always-on `daemon run` service
			nil,             // web update: only the always-on `daemon run` service acts on it
			nil,             // ask_peer consult: answered by the always-on `daemon run` service, not the manager
			nil,             // ask_peer consult_answer: surfaced by the always-on `daemon run` service
			nil)             // ask_peer consult_cancel: the pending set it would drop from belongs to `daemon run`
		if ctx.Err() != nil {
			return
		}
		if err == api.ErrDaemonRevoked {
			p.set(presError, "device revoked — ptln daemon enable")
			return
		}
		p.set(presConnecting, "reconnecting…")
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// remirror pushes the current joinable labels to the control plane and refreshes the
// advertising count. Called on connect and whenever the joinable set changes while Online.
// Quiet + best-effort (never prints — the mux owns the screen).
func (p *daemonPresence) remirror(d daemonDevice) {
	reg := loadDaemonRegistry()
	refs := make([]api.DaemonProjectRef, 0, len(reg.Projects))
	for _, pr := range reg.Projects {
		refs = append(refs, api.DaemonProjectRef{Label: pr.Label, Preset: pr.Preset})
	}
	_ = api.MirrorProjects(d.Base, d.Token, refs)
	p.mu.Lock()
	p.advertising = len(refs)
	p.mu.Unlock()
}

// dropPending removes a request from the approval queue (after approve/deny/execute).
func (p *daemonPresence) dropPending(reqID string) {
	p.mu.Lock()
	kept := p.pendingReqs[:0]
	for _, q := range p.pendingReqs {
		if q.reqID != reqID {
			kept = append(kept, q)
		}
	}
	p.pendingReqs = kept
	p.mu.Unlock()
}

// approve authorizes a pending request (flips it to accepted server-side). Execution follows
// on the `accepted` event (executeAccepted) — the same path the web modal triggers. Network
// I/O runs off the keypress goroutine; the banner clears optimistically.
func (p *daemonPresence) approve(reqID, label string) {
	p.dropPending(reqID)
	if d := loadDaemonDevice(); d.Token != "" {
		go func() {
			_ = daemonAuthorize(d, label, reqID)
			p.repaint()
		}()
	}
}

// deny declines a pending request (terminal). Best-effort + optimistic, like approve.
func (p *daemonPresence) deny(reqID string) {
	p.dropPending(reqID)
	if d := loadDaemonDevice(); d.Token != "" {
		go func() {
			_ = api.SetLaunchStatus(d.Base, d.Token, reqID, "declined", "declined by owner")
			p.repaint()
		}()
	}
}

// joinableChanged is called after a [P] cycle. Mirroring is a stateless PUT (independent of who
// holds the stream), so we re-advertise whenever a device is enrolled — that keeps a teammate's
// picker current under manager-Online AND under Always-on, where the background service only
// mirrors at its own startup and wouldn't otherwise see this registry edit.
func (p *daemonPresence) joinableChanged() {
	if d := loadDaemonDevice(); d.Token != "" {
		go p.remirror(d)
	}
}
