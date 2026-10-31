package main

// tmux hosts every session: bare `ptln` attaches and opens the launcher
// popup; `ptln --resume`, `ptln llms <id>...` and `ptln llms new ...` open straight into tmux;
// inside the ptln tmux session, opens become new windows on the same server.
//
// Workspace fidelity: every PANE the backend creates carries its Spec (base64 JSON in the pane
// option @ptln_spec); a client-detached/window-unlinked hook runs `ptln tmux --save-workspace`,
// which reads those options back and writes llms-workspace.json — so `--resume` restores it.
//
// Per-pane rather than per-window because two sessions can be merged into one window from the
// ctrl-\ menu: the pane moves, and a window option would stay behind with the window the session
// left. Panes sharing a window are saved with a common Group and that window's layout string, so
// a resume rebuilds the split — same sides, same widths — instead of scattering it back into a
// window each. A session alone in its window carries neither field, which is what every workspace
// file written before merging existed already looks like.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// probeTmuxVersion returns the installed version or describes why it is unavailable.
var probeTmuxVersion = func() string {
	if _, err := exec.LookPath("tmux"); err != nil {
		return "no tmux on PATH"
	}
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return "tmux version unavailable"
	}
	return strings.TrimSpace(string(out))
}

func requireTmux() error {
	found := probeTmuxVersion()
	if !tmuxVersionOK(found) {
		return fmt.Errorf("ptln requires tmux 3.3 or newer (found: %s). Install or upgrade with `brew install tmux` on macOS or your distro package manager (e.g. `sudo apt install tmux`)", found)
	}
	return nil
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
// errNothingToOpen is tmuxMerge with no restorable spec: every one was an obsolete fixture or not resumable.
var errNothingToOpen = errors.New("no resumable sessions to open")

func runTmuxApp(specs []Spec) error {
	if err := requireTmux(); err != nil {
		return err
	}
	specs = dropHeldForks(specs)
	refreshTmuxBackground()
	if err := writeTmuxConf(); err != nil {
		return err
	}
	first, err := tmuxMerge(specs)
	if err != nil {
		if !errors.Is(err, errNothingToOpen) {
			return err
		}
		first, err = tmuxShellWindow()
		if err != nil {
			return err
		}
	}
	retireFixtureWindows()
	// A server born under an older binary keeps its old conf; re-source so this binary's
	// chords/hooks apply to it too.
	_ = tmuxCmd("source-file", filepath.Join(stateDir(), "tmux.conf")).Run()
	// A new or resumed server starts with no loop indicators; rebuild them from the runs on disk.
	loopRefreshIndicators()
	ensureWatcher()
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
func tmuxMerge(specs []Spec) (string, error) {
	cwd, _ := os.Getwd()
	// Keyed by @ptln_key, valued by PANE — a merged session is a pane in a window it shares,
	// so a window id could no longer name it. Read through list-panes for the same reason.
	existing := map[string]string{}
	if out, err := tmuxCmd("list-panes", "-s", "-t", tmuxSessionName, "-F", "#{pane_id}\t#{window_id}\t#{@ptln_key}").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f := strings.SplitN(line, "\t", 3)
			if len(f) != 3 || f[2] == "" {
				continue
			}
			existing[f[2]] = f[0]
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
		if len(sp.Argv) == 0 || !restorableSpec(sp) {
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
		// IDENTITY, carried into the pane's environment.
		//
		// PARTYLINE_SESSION_KEY is how a child — and anything it spawns, notably the cg-mcp server —
		// knows WHICH session it is. ptymux exports it in childEnv; the tmux backend tagged the pane
		// with @ptln_key and set nothing in the environment, so cg-mcp read an empty string and every
		// ask_session and list_sessions call refused with "this session isn't running inside a ptln
		// window" — on the DEFAULT backend, while sitting in a ptln window. The roster, the store and
		// the watcher were all already wired; only this was missing.
		//
		// `-e` is per-pane, so a session can never inherit a sibling's identity and steer a delivery
		// at the wrong agent. That is the same property childEnv gets by stripping inherited values.
		ident := tmuxIdentityEnv(sp)

		var args []string
		switch {
		case sp.Group != "" && groupPane[sp.Group] != "":
			// a companion of a session already open — split its window rather than take a new one
			args = append([]string{"split-window", "-d", "-h", "-P", "-F", "#{pane_id}", "-t", groupPane[sp.Group], "-c", dir}, append(ident, "--")...)
		case !haveSession:
			args = append([]string{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", tmuxSessionName, "-n", tmuxWindowName(sp.Label), "-c", dir}, append(ident, "--")...)
		default:
			args = append([]string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", tmuxSessionName + ":", "-n", tmuxWindowName(sp.Label), "-c", dir}, append(ident, "--")...)
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
		return "", errNothingToOpen
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
func tagTmuxPane(paneID string, sp Spec) {
	b, err := json.Marshal(sp)
	if err != nil {
		return
	}
	_ = tmuxCmd("set-option", "-p", "-t", paneID, "@ptln_key", sp.Key).Run()
	_ = tmuxCmd("set-option", "-p", "-t", paneID, "@ptln_spec", base64.StdEncoding.EncodeToString(b)).Run()
}

// paneSpec reads one pane's Spec. ok=false for an untagged pane (a bare shell, a split someone
// made themselves) and obsolete fixture windows.
func paneSpec(paneID string) (Spec, bool) {
	out, err := tmuxCmd("display-message", "-p", "-t", paneID, "#{@ptln_spec}").Output()
	if err != nil {
		return Spec{}, false
	}
	return decodePaneSpec(strings.TrimSpace(string(out)))
}

func decodePaneSpec(enc string) (Spec, bool) {
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return Spec{}, false
	}
	var sp Spec
	if json.Unmarshal(b, &sp) != nil || len(sp.Argv) == 0 || !restorableSpec(sp) {
		// Obsolete fixtures are never restored from the workspace.
		return Spec{}, false
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
func tmuxWorkspaceSpecs() []Spec {
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

	var specs []Spec
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
	spec = prepareLaunchSpec(spec)
	target, err := tmuxMerge([]Spec{spec})
	if err != nil {
		return err
	}
	return tmuxFocus(target)
}

// tagTmuxWindow tags a window for workspace saves and session discovery.
func tagTmuxWindow(windowID string, sp Spec) {
	b, err := json.Marshal(sp)
	if err != nil {
		return
	}
	_ = tmuxCmd("set-option", "-w", "-t", windowID, "@ptln_key", sp.Key).Run()
	_ = tmuxCmd("set-option", "-w", "-t", windowID, "@ptln_spec", base64.StdEncoding.EncodeToString(b)).Run()
}

// tmuxHome opens the session popup on the attached client.
func tmuxHome() error {
	if err := requireTmux(); err != nil {
		return err
	}
	cols, rows := tmuxClientSize()
	if cols > 0 && rows > 0 {
		return tmuxCmd(launcherPopupCommand(selfExe(), cols, rows)...).Run()
	}
	return fmt.Errorf("no attached tmux client")
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

// tmuxIdentityEnv is the `-e` flags that give a pane its session identity. One definition, used by
// every path that starts or restarts a session, so a pane can never come up without it.
func tmuxIdentityEnv(sp Spec) []string {
	env := []string{"-e", "PARTYLINE=1"}
	if sp.Key != "" {
		env = append(env, "-e", "PARTYLINE_SESSION_KEY="+sp.Key)
	}
	if sp.Label != "" {
		env = append(env, "-e", "PARTYLINE_SESSION_LABEL="+sp.Label)
	}
	return env
}

// tmuxRenameForPanes names a window after the sessions actually in it.
//
// The window name is what the tab ribbon shows, and it used to be whoever opened the window first.
// So merging two sessions produced one tab reading "ACR POS" with no hint that a codex session was
// sitting beside it — and after the first session was closed the name outlived it entirely, leaving
// a tab called "ACR BACKLOG !" holding nothing of the sort.
//
// Composite for a pair, plain for a lone session, so unsplitting restores the simple name without a
// second code path. Auxiliary panes (the feed, a shell) carry no spec and are skipped: a session
// beside its feed is still one session, the same rule parseSessionPanes applies.
//
// A pane partyline did not start (an engine run by hand in a shell) has no spec. It is named after
// the engine running on its terminal and its directory, "codex · devopsforcitizendevs". Returning
// early instead, as this once did, left a tab named after the session that had just moved OUT of it:
// a Codex pane sat under "Partyline Original" because that session had shared its window. A window
// with nothing nameable at all gets tmux's automatic naming back.
func tmuxRenameForPanes(window string) {
	out, err := tmuxCmd("list-panes", "-t", window, "-F", "#{@ptln_spec}\t#{pane_tty}\t#{pane_current_path}").Output()
	if err != nil {
		return
	}
	var labels []string
	// Trim only the newline: a pane with no spec prints an empty first field, so its line starts
	// with a tab, and TrimSpace would shift every field by one.
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if sp, ok := decodePaneSpec(strings.TrimSpace(f[0])); ok && sp.Label != "" {
			labels = append(labels, sp.Label)
			continue
		}
		if len(f) == 3 {
			if eng := paneEngine(f[1]); eng != "" {
				labels = append(labels, eng+" · "+filepath.Base(f[2]))
			}
		}
	}
	if len(labels) == 0 {
		_ = tmuxCmd("set-option", "-w", "-t", window, "automatic-rename", "on").Run()
		return
	}
	name := labels[0]
	for _, l := range labels[1:] {
		name += " │ " + l
	}
	_ = tmuxCmd("rename-window", "-t", window, tmuxPairWindowName(name)).Run()
}

// tmuxPairWindowName is tmuxWindowName with room for two names. One session keeps the old 24-column
// budget; a pair gets more, because truncating "ACR POS │ codex · acr-cloud" back to 24 would hide
// the very thing the composite exists to show.
func tmuxPairWindowName(label string) string {
	s := strings.TrimSpace(tmuxUnprintable.ReplaceAllString(label, ""))
	if s == "" {
		return "session"
	}
	limit := 24
	if strings.Contains(s, "│") {
		limit = 44
	}
	if len([]rune(s)) > limit {
		s = string([]rune(s)[:limit])
	}
	return s
}
