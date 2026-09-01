package main

// tmuxTargets is the tmux transport for the session watchers (ask-peer delivery,
// ask-session): the same watcher code that reaches built-in-mux sessions through muxTargets
// reaches tmux-hosted sessions through this. The launcher fixture is the resident process
// that runs the watchers; the sessions are panes tagged with their llms id (@ptln_key).
//
// Safety posture differs in ONE deliberate way: UnsubmittedInput is unknowable from outside
// the pane (tmux owns the keystrokes), so it reports unknown — which the delivery policy
// already treats as "stage, don't submit". A peer answer lands staged; the human presses
// Enter. Degraded exactly toward safety.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/ptymux"
)

type tmuxTargets struct{}

// tmuxBannerAt is when SetBanner last fired, shared across the (stateless) target values —
// BannerActive's whole job is stopping one watcher's banner from stomping another's while
// it is still readable on the status line (display-time in the conf).
var tmuxBannerAt struct {
	mu sync.Mutex
	at time.Time
}

// paneForKey maps an llms session id to its pane.
//
// Per-pane, not per-window: two merged sessions share a window, so the window's active pane is
// whichever one the human last clicked — delivering a peer answer there would inject it into
// the wrong agent.
func paneForKey(key string) (pane, label string, ok bool) {
	out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F", "#{@ptln_key}\t#{pane_id}\t#{window_name}\t#{@ptln_spec}").Output()
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) == 4 && f[0] == key {
			label := f[2]
			// the window name is shared by a merged pair; the spec's own label is not
			if sp, ok := decodePaneSpec(f[3]); ok && sp.Label != "" {
				label = sp.Label
			}
			return f[1], label, true
		}
	}
	return "", "", false
}

func (tmuxTargets) SessionByKey(key string) (sessIO, string, string, bool) {
	pane, label, ok := paneForKey(key)
	if !ok {
		return nil, "", "", false
	}
	dir := ""
	if out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{pane_current_path}").Output(); err == nil {
		dir = strings.TrimSpace(string(out))
	}
	return tmuxPane{id: pane}, label, dir, true
}

// UnsubmittedInput: tmux owns the keystrokes, so typed-but-unsent bytes are unknowable
// directly. But typing ECHOES — a human mid-keystroke makes the pane emit output — so window
// activity is an honest proxy: fresh activity → unknown (the watchers wait and retry), quiet
// for a few seconds → "no evidence of typing" (0, true), the same no-evidence standard the
// built-in mux's own check documents for itself. Without this, ask_session — which MUST
// submit, a question nobody sends is never answered — would time out on every tmux session.
func (tmuxTargets) UnsubmittedInput(key string) (int, bool) {
	pane, _, ok := paneForKey(key)
	if !ok {
		return 0, false
	}
	out, err := tmuxCmd("display-message", "-p", "-t", pane, "#{window_activity}").Output()
	if err != nil {
		return 0, false
	}
	var at int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &at); err != nil {
		return 0, false
	}
	if time.Since(time.Unix(at, 0)) < tmuxQuietWindow {
		return 0, false // recent output — could be the echo of someone typing; wait
	}
	return 0, true
}

// tmuxQuietWindow: how long a pane must be silent before "nobody is typing" is claimed.
const tmuxQuietWindow = 3 * time.Second

func (tmuxTargets) SessionStatus(key string) string {
	for _, s := range collectSessions() {
		if s.ID == key {
			return liveStatus(s)
		}
	}
	return ""
}

func (tmuxTargets) SetBanner(s string) {
	tmuxBannerAt.mu.Lock()
	tmuxBannerAt.at = time.Now()
	tmuxBannerAt.mu.Unlock()
	_ = tmuxCmd("display-message", s).Run()
}

func (tmuxTargets) BannerActive() bool {
	tmuxBannerAt.mu.Lock()
	defer tmuxBannerAt.mu.Unlock()
	return time.Since(tmuxBannerAt.at) < 6*time.Second // matches the conf's display-time
}

// ActiveThread: the focused window's session thread, read back from its stored Spec — the
// checkup follows whatever the human is looking at, same as the built-in mux.
func (tmuxTargets) ActiveThread() string {
	out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{@ptln_spec}").Output()
	if err != nil {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return ""
	}
	var sp ptymux.Spec
	if json.Unmarshal(b, &sp) != nil {
		return ""
	}
	return sp.Thread
}

// IdleSince: how long since the HUMAN did anything — tmux tracks exactly this per client
// (client_activity is input activity, not output). No attached client means away.
func (tmuxTargets) IdleSince() time.Duration {
	out, err := tmuxCmd("list-clients", "-F", "#{client_activity}").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return 24 * time.Hour
	}
	best := 24 * time.Hour
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var at int64
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &at); err == nil {
			if d := time.Since(time.Unix(at, 0)); d < best {
				best = d
			}
		}
	}
	return best
}

func (tmuxTargets) LiveSessions() []ptymux.LiveSession {
	out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F", "#{@ptln_key}\t#{window_name}\t#{@ptln_spec}").Output()
	if err != nil {
		return nil
	}
	var live []ptymux.LiveSession
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 || f[0] == "" || f[0] == tmuxLauncherKey {
			continue
		}
		label := f[1]
		if sp, ok := decodePaneSpec(f[2]); ok && sp.Label != "" {
			label = sp.Label
		}
		live = append(live, ptymux.LiveSession{Key: f[0], Label: label})
	}
	return live
}

// tmuxPane is sessIO over one pane.
type tmuxPane struct{ id string }

// WriteInput sends raw bytes — send-keys -l passes them uninterpreted, so the bracketed-paste
// fence and the submit CR arrive exactly as the built-in mux would have written them.
func (p tmuxPane) WriteInput(b []byte) {
	_ = tmuxCmd("send-keys", "-t", p.id, "-l", "--", string(b)).Run()
}

func (p tmuxPane) Snapshot() []byte {
	out, err := tmuxCmd("capture-pane", "-p", "-t", p.id).Output()
	if err != nil {
		return nil
	}
	return out
}

func (p tmuxPane) SnapshotHistory(maxLines, _ int) []byte {
	out, err := tmuxCmd("capture-pane", "-p", "-t", p.id, "-S", "-"+itoa(maxLines)).Output()
	if err != nil {
		return nil
	}
	return out
}
