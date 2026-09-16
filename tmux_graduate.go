package main

// tmux as the DEFAULT session host (the graduation of the `ptln tmux` prototype).
//
// The built-in pass-through mux corrupts the screen when it switches between live
// differential-redraw TUIs — a structural defect (see internal/ptymux/mux.go's own account).
// tmux keeps a full server-side grid per window, so hosting the LIVE sessions there ends the
// corruption class. The launcher keeps its existing UI: the defect needs live children, and
// in tmux mode the built-in mux never hosts any — it only ever draws the launcher.
//
// Routing (when tmux is installed and PARTYLINE_MUX != "classic"):
//   - `ptln --resume`, `ptln llms <id>...`, `ptln llms new ...` → straight into tmux
//   - bare `ptln` → the launcher as before; opening a session shells out to a tmux attach
//     (the mux's Suspend flow: terminal restored, attach runs, detach returns to the launcher)
//   - inside the ptln tmux session, opens become new windows on the same server
//
// Workspace fidelity: every PANE the backend creates carries its Spec (base64 JSON in the pane
// option @ptln_spec); a client-detached/window-unlinked hook runs `ptln tmux --save-workspace`,
// which reads those options back and writes the same llms-workspace.json the built-in mux saves
// at quit — so `--resume` keeps working across backends, in both directions.
//
// Per-pane rather than per-window because two sessions can be merged into one window from the
// ctrl-\ menu: the pane moves, and a window option would stay behind with the window the session
// left. Panes sharing a window are saved with a common Group and that window's layout string, so
// a resume rebuilds the split — same sides, same widths — instead of scattering it back into a
// window each. A session alone in its window carries neither field, which is what every workspace
// file written before merging existed already looks like.
//
// PARTYLINE_MUX=classic restores the built-in mux wholesale.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"partyline.sh/partyline/internal/ptymux"
)

// useTmuxBackend reports whether live sessions should be hosted in tmux — the DEFAULT when
// a usable tmux is installed, now that the backend carries the full application surface
// (injection, ask-session, banners, menus, sharing, scribe). PARTYLINE_MUX=classic restores
// the built-in mux wholesale. Requires tmux ≥ 3.3: the generated conf uses popup styling
// and `prefix None`, and an older tmux aborts the conf mid-file — worse than no tmux at all.
func useTmuxBackend() bool {
	if strings.TrimSpace(os.Getenv("PARTYLINE_MUX")) == "classic" {
		return false
	}
	return tmuxUsable()
}

// tmuxUsable: installed and modern enough for the generated conf.
func tmuxUsable() bool {
	if _, err := exec.LookPath("tmux"); err != nil {
		return false
	}
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return false
	}
	return tmuxVersionOK(strings.TrimSpace(string(out)))
}

// tmuxVersionOK parses "tmux 3.7c" / "tmux 3.3a" / "tmux next-3.6" and demands ≥ 3.3.
func tmuxVersionOK(v string) bool {
	v = strings.TrimPrefix(v, "tmux ")
	v = strings.TrimPrefix(v, "next-")
	major, minor := 0, 0
	if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil {
		return false
	}
	return major > 3 || (major == 3 && minor >= 3)
}

// insidePtlnTmux reports whether this process is already running inside the backend's own
// tmux server (never a user's personal tmux — $TMUX carries the socket path, and ours is
// always the "partyline" socket).
func insidePtlnTmux() bool {
	env := os.Getenv("TMUX")
	if env == "" {
		return false
	}
	sock, _, _ := strings.Cut(env, ",")
	return filepath.Base(sock) == tmuxSocketName()
}

// tmuxCmd builds a command against the backend's private server. The socket is passed
// explicitly even from inside tmux, so hook-spawned children (which may or may not inherit
// $TMUX) always land on the right server. The conf rides along too: tmux only reads -f on
// the command that STARTS the server, and any tmuxCmd call can be that command (tmuxMerge's
// first new-session usually is) — dropping it here once shipped a server with no branding,
// no chords, and no save hooks.
func tmuxCmd(args ...string) *exec.Cmd {
	pre := []string{"-L", tmuxSocketName()}
	if conf := filepath.Join(stateDir(), "tmux.conf"); fileExists(conf) {
		pre = append(pre, "-f", conf)
	}
	return exec.Command("tmux", append(pre, args...)...)
}

// runTmuxApp hosts the given sessions in tmux: existing windows are reused (matched by
// @ptln_key), missing ones are created, and the client attaches — or, when already inside
// the backend's tmux, just switches to the first requested window.
func runTmuxApp(specs []ptymux.Spec) error {
	specs = dropHeldForks(specs)
	if len(specs) == 0 {
		return fmt.Errorf("nothing to open")
	}
	if err := writeTmuxConf(); err != nil {
		return err
	}
	first, err := tmuxMerge(specs)
	if err != nil {
		return err
	}
	ensureLauncherWindow()
	// A server born under an older binary keeps its old conf; re-source so this binary's
	// chords/hooks apply to it too.
	_ = tmuxCmd("source-file", filepath.Join(stateDir(), "tmux.conf")).Run()
	if insidePtlnTmux() {
		return tmuxCmd("switch-client", "-t", first).Run()
	}
	if err := tmuxFocus(first); err != nil {
		return err
	}
	attach := tmuxCmd("attach-session", "-t", tmuxSessionName)
	attach.Stdin, attach.Stdout, attach.Stderr = os.Stdin, os.Stdout, os.Stderr
	return attach.Run()
}

// writeTmuxConf regenerates the backend's conf (idempotent; rewritten every launch so conf
// improvements ship with the binary).
func writeTmuxConf() error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir(), "tmux.conf"), []byte(tmuxConf()), 0o644)
}

// tmuxMerge ensures a window exists for every spec, creating the session on first need.
// Returns the target (window id) of the first spec. Windows are matched by @ptln_key so
// reopening an already-open session switches instead of duplicating — the same dedupe rule
// as the built-in mux's spawnOrSwitch.
func tmuxMerge(specs []ptymux.Spec) (string, error) {
	cwd, _ := os.Getwd()
	// Keyed by @ptln_key, valued by PANE — a merged session is a pane in a window it shares,
	// so a window id could no longer name it. Read through list-panes for the same reason.
	existing := map[string]string{}
	launcher := ""
	if out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F", "#{pane_id}\t#{@ptln_key}").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if id, key, ok := strings.Cut(line, "\t"); ok && key != "" {
				existing[key] = id
				if key == tmuxLauncherKey {
					launcher = id
				}
			}
		}
	}
	haveSession := tmuxCmd("has-session", "-t", tmuxSessionName).Run() == nil

	// Restoring a group: the first spec opens a window, the rest split into it, and the saved
	// layout is applied once the group is whole (so pane sizes survive a detach, not just the
	// fact of the split).
	groupPane := map[string]string{} // Group → a pane already open in that group's window
	groupLayout := map[string]string{}

	first := ""
	for _, sp := range specs {
		if len(sp.Argv) == 0 {
			continue
		}
		if sp.Group != "" && sp.Layout != "" {
			groupLayout[sp.Group] = sp.Layout
		}
		if sp.Key != "" {
			if id, ok := existing[sp.Key]; ok {
				if sp.Group != "" && groupPane[sp.Group] == "" {
					groupPane[sp.Group] = id
				}
				if first == "" {
					first = id
				}
				continue
			}
		}
		dir := sp.Dir
		if dir == "" {
			dir = cwd
		}
		var args []string
		switch {
		case sp.Group != "" && groupPane[sp.Group] != "":
			// a companion of a session already open — split its window rather than take a new one
			args = []string{"split-window", "-d", "-h", "-P", "-F", "#{pane_id}", "-t", groupPane[sp.Group], "-c", dir, "--"}
		case !haveSession:
			args = []string{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "-n", tmuxWindowName(sp.Label), "-c", dir, "--"}
		case launcher != "":
			// sessions open to the LEFT of the launcher — it is a fixture at the far right
			args = []string{"new-window", "-d", "-b", "-P", "-F", "#{pane_id}", "-t", launcher, "-n", tmuxWindowName(sp.Label), "-c", dir, "--"}
		default:
			args = []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmuxSessionName, "-n", tmuxWindowName(sp.Label), "-c", dir, "--"}
		}
		args = append(args, sp.Argv...)
		out, err := tmuxCmd(args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("open %q: %v\n%s", sp.Label, err, out)
		}
		haveSession = true
		id := strings.TrimSpace(string(out))
		tagTmuxPane(id, sp)
		if sp.Key != "" {
			existing[sp.Key] = id
		}
		if sp.Group != "" && groupPane[sp.Group] == "" {
			groupPane[sp.Group] = id
		}
		if first == "" {
			first = id
		}
	}
	// Best-effort: tmux rejects a layout string whose checksum does not match the panes present,
	// which is exactly what happens when one session of a group failed to open. An even split is
	// a fine outcome there; a hard failure would not be.
	for group, layout := range groupLayout {
		if pane := groupPane[group]; pane != "" {
			_ = tmuxCmd("select-layout", "-t", pane, layout).Run()
		}
	}
	if first == "" {
		return "", fmt.Errorf("no resumable sessions to open")
	}
	return first, nil
}

// tmuxFocus puts the human on a target that may be a pane. select-window alone resolves a pane
// id to its window but leaves the OTHER pane of a split active, which lands you on the session
// next to the one you asked for.
func tmuxFocus(target string) error {
	if err := tmuxCmd("select-window", "-t", target).Run(); err != nil {
		return err
	}
	_ = tmuxCmd("select-pane", "-t", target).Run()
	return nil
}

// tagTmuxPane stores a session's Spec on its PANE (base64 JSON — safe through tmux's option
// quoting), which is what --save-workspace reads back.
//
// It hangs off the pane rather than the window because merging moves a pane between windows:
// a window option would stay behind with the window the session left, and the session would
// lose its identity the moment someone put it side by side with another.
//
// Reading is always through format expansion (#{@ptln_spec}), never show-options -p, because
// only the format path walks pane → window → session. That inheritance is what lets a binary
// with per-pane tagging read a server whose windows an older binary tagged, so `ptln update`
// does not orphan the sessions already running.
func tagTmuxPane(paneID string, sp ptymux.Spec) {
	b, err := json.Marshal(sp)
	if err != nil {
		return
	}
	_ = tmuxCmd("set-option", "-p", "-t", paneID, "@ptln_key", sp.Key).Run()
	_ = tmuxCmd("set-option", "-p", "-t", paneID, "@ptln_spec", base64.StdEncoding.EncodeToString(b)).Run()
}

// paneSpec reads one pane's Spec. ok=false for an untagged pane (a bare shell, a split someone
// made themselves) and for the launcher fixture.
func paneSpec(paneID string) (ptymux.Spec, bool) {
	out, err := tmuxCmd("display-message", "-p", "-t", paneID, "#{@ptln_spec}").Output()
	if err != nil {
		return ptymux.Spec{}, false
	}
	return decodePaneSpec(strings.TrimSpace(string(out)))
}

func decodePaneSpec(enc string) (ptymux.Spec, bool) {
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return ptymux.Spec{}, false
	}
	var sp ptymux.Spec
	if json.Unmarshal(b, &sp) != nil || len(sp.Argv) == 0 || sp.Key == tmuxLauncherKey {
		// the launcher is a fixture, recreated on every open — never part of the workspace
		return ptymux.Spec{}, false
	}
	return sp, true
}

// tmuxWorkspaceSpecs reads every tagged pane's Spec back off the server — the tmux-side
// equivalent of the built-in mux's liveSpecs().
//
// Panes sharing a window are stamped with a common Group and the window's layout string, so
// `--resume` rebuilds the split the human left instead of scattering it back into one window
// each. A window holding a single session gets neither, which keeps the saved file identical
// to what earlier versions wrote for an unmerged workspace.
func tmuxWorkspaceSpecs() []ptymux.Spec {
	// Sorted by screen position, not pane index. The two diverge as soon as a pane is joined in
	// on the left, and restoring in index order would then bring the split back with the sessions
	// on opposite sides from where the human left them.
	out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F",
		"#{window_id}\t#{window_layout}\t#{pane_top}\t#{pane_left}\t#{@ptln_spec}").Output()
	if err != nil {
		return nil
	}
	type row struct {
		win, layout, enc string
		order, top, left int // order = the window's position in the ribbon
	}
	var rows []row
	winOrder := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 5)
		if len(f) != 5 || f[4] == "" {
			continue
		}
		if _, seen := winOrder[f[0]]; !seen {
			winOrder[f[0]] = len(winOrder) // list-panes -s walks windows in ribbon order
		}
		top, _ := strconv.Atoi(f[2])
		left, _ := strconv.Atoi(f[3])
		rows = append(rows, row{win: f[0], layout: f[1], enc: f[4], order: winOrder[f[0]], top: top, left: left})
	}
	// Windows keep their ribbon order; panes within one are ordered by where they sit on screen.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.order != b.order {
			return a.order < b.order
		}
		if a.top != b.top {
			return a.top < b.top
		}
		return a.left < b.left
	})

	var specs []ptymux.Spec
	perWindow := map[string][]int{} // window id → indices into specs
	layouts := map[string]string{}
	for _, r := range rows {
		sp, ok := decodePaneSpec(r.enc)
		if !ok {
			continue
		}
		layouts[r.win] = r.layout
		perWindow[r.win] = append(perWindow[r.win], len(specs))
		specs = append(specs, sp)
	}
	for win, idx := range perWindow {
		if len(idx) < 2 {
			continue
		}
		group := specs[idx[0]].Key
		if group == "" {
			group = win
		}
		for _, i := range idx {
			specs[i].Group = group
		}
		specs[idx[0]].Layout = layouts[win]
	}
	return specs
}

// tmuxSaveWorkspace is the client-detached / window-unlinked hook target: snapshot the open
// windows into llms-workspace.json so `--resume` reflects the last detach, exactly as the
// built-in mux's BeforeQuit snapshot does. saveWorkspace's empty-set guard applies here too
// (closing everything must not wipe a resumable workspace).
func tmuxSaveWorkspace() {
	saveWorkspace(tmuxWorkspaceSpecs())
}

// tmuxNewWindow is the n/N chord target (run-shell from inside tmux): build the same quick
// spec the launcher's N key builds — engine, current pane's directory, optional bypass —
// open it as a tagged window, and focus it.
func tmuxNewWindow(bypass bool) error {
	dir, _ := os.Getwd()
	if out, err := tmuxCmd("display-message", "-p", "-t", tmuxSessionName, "#{pane_current_path}").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" {
			dir = d
		}
	}
	tool := quickNewEngine()
	var perm []string
	if bypass {
		perm = bypassFlagsFor(tool)
	}
	spec, err := newSessionSpec(tool, dir, "", "", "", false, false, 0, perm...)
	if err != nil {
		return err
	}
	spec = inheritRepoBindSpec(spec)
	target, err := tmuxMerge([]ptymux.Spec{spec})
	if err != nil {
		return err
	}
	return tmuxFocus(target)
}

// isLauncherWindow reports whether a window (or the window holding a pane) is the launcher
// fixture — a read, never a create.
func isLauncherWindow(target string) bool {
	if target == "" {
		return false
	}
	out, err := tmuxCmd("display-message", "-p", "-t", target, "#{@ptln_key}").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == tmuxLauncherKey
}

// tmuxLauncherKey tags the launcher fixture: always present, pinned at the far right,
// never closable, never saved into the workspace.
const tmuxLauncherKey = "__launcher__"

// ensureLauncherWindow makes sure the launcher fixture exists (bare ptln — the full session
// browser) and returns its window id. It is created LAST so it sits at the far right; merge
// inserts every session window before it, which keeps it there.
func ensureLauncherWindow() string {
	out, _ := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_id}\t#{window_index}\t#{@ptln_key}").Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	maxIdx := ""
	found := ""
	for _, line := range lines {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		maxIdx = f[1] // list-windows is index-ordered; the last line has the highest
		if f[2] == tmuxLauncherKey {
			found = f[0]
		}
	}
	if found != "" {
		// a fixture that drifted off the far right (older binary, manual moves) heals here
		if len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\t"+tmuxLauncherKey) {
			_ = tmuxCmd("move-window", "-s", found, "-t", tmuxSessionName+":"+nextIndex(maxIdx)).Run()
		}
		return found
	}
	cwd, _ := os.Getwd()
	id, err := tmuxCmd("new-window", "-d", "-P", "-F", "#{window_id}", "-t", tmuxSessionName, "-n", "⌂ launcher", "-c", cwd, "--", selfExe()).Output()
	if err != nil {
		return ""
	}
	win := strings.TrimSpace(string(id))
	tagTmuxWindow(win, ptymux.Spec{Label: "⌂ launcher", Key: tmuxLauncherKey, Argv: []string{selfExe()}, Dir: cwd})
	return win
}

// tagTmuxWindow tags at WINDOW scope. Only the launcher fixture is tagged this way now: it is
// never merged, never moved between windows, and ensureLauncherWindow needs to find it in window
// order to keep it pinned at the far right.
//
// It is also the shape every session carried before merging existed, which is why the readers
// go through format expansion — a pane inherits its window's tag, so this binary reads a server
// an older one tagged, and `ptln update` does not orphan the sessions already running.
func tagTmuxWindow(windowID string, sp ptymux.Spec) {
	b, err := json.Marshal(sp)
	if err != nil {
		return
	}
	_ = tmuxCmd("set-option", "-w", "-t", windowID, "@ptln_key", sp.Key).Run()
	_ = tmuxCmd("set-option", "-w", "-t", windowID, "@ptln_spec", base64.StdEncoding.EncodeToString(b)).Run()
}

// tmuxHome jumps to the launcher fixture (the ctrl-\ o of the built-in mux), creating it if
// something managed to kill it.
func tmuxHome() error {
	id := ensureLauncherWindow()
	if id == "" {
		return fmt.Errorf("could not open the launcher window")
	}
	return tmuxCmd("select-window", "-t", id).Run()
}

// nextIndex is maxIdx+1 as a string (window indexes are small decimal ints).
func nextIndex(maxIdx string) string {
	n := 0
	fmt.Sscanf(maxIdx, "%d", &n)
	return fmt.Sprintf("%d", n+1)
}

// tmuxPaneCount reports how many panes share the target's window — 1 for a session that has
// not been merged with anything. Callers use it to keep window-wide effects (renaming, for one)
// off a window that belongs to two sessions.
func tmuxPaneCount(target string) int {
	out, err := tmuxCmd("list-panes", "-t", target, "-F", "#{pane_id}").Output()
	if err != nil {
		return 1
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}
