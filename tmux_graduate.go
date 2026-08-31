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
// Workspace fidelity: every window the backend creates carries its Spec (base64 JSON in the
// window option @ptln_spec); a client-detached/window-unlinked hook runs
// `ptln tmux --save-workspace`, which reads those options back and writes the same
// llms-workspace.json the built-in mux saves at quit — so `--resume` keeps working across
// backends, in both directions.
//
// PARTYLINE_MUX=classic restores the built-in mux wholesale.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/ptymux"
)

// useTmuxBackend reports whether live sessions should be hosted in tmux. OPT-IN for now:
// PARTYLINE_MUX=tmux (or the explicit `ptln tmux` command, which doesn't consult this).
// The built-in mux stays the default until the tmux backend reaches functional parity —
// ask-peer injection, ask-session, banners, sharing, scribe — not just visual parity.
// Requires tmux ≥ 3.3: the generated conf uses popup styling and `prefix None`, and an
// older tmux aborts the conf mid-file, which is worse than no tmux at all.
func useTmuxBackend() bool {
	if strings.TrimSpace(os.Getenv("PARTYLINE_MUX")) != "tmux" {
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
	if err := tmuxCmd("select-window", "-t", first).Run(); err != nil {
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
	existing := map[string]string{} // @ptln_key → window id
	launcher := ""
	if out, err := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{window_id}\t#{@ptln_key}").Output(); err == nil {
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

	first := ""
	for _, sp := range specs {
		if len(sp.Argv) == 0 {
			continue
		}
		if sp.Key != "" {
			if id, ok := existing[sp.Key]; ok {
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
		case !haveSession:
			args = []string{"new-session", "-d", "-P", "-F", "#{window_id}", "-s", tmuxSessionName, "-n", tmuxWindowName(sp.Label), "-c", dir, "--"}
		case launcher != "":
			// sessions open to the LEFT of the launcher — it is a fixture at the far right
			args = []string{"new-window", "-d", "-b", "-P", "-F", "#{window_id}", "-t", launcher, "-n", tmuxWindowName(sp.Label), "-c", dir, "--"}
		default:
			args = []string{"new-window", "-d", "-P", "-F", "#{window_id}", "-t", tmuxSessionName, "-n", tmuxWindowName(sp.Label), "-c", dir, "--"}
		}
		args = append(args, sp.Argv...)
		out, err := tmuxCmd(args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("open %q: %v\n%s", sp.Label, err, out)
		}
		haveSession = true
		id := strings.TrimSpace(string(out))
		tagTmuxWindow(id, sp)
		if sp.Key != "" {
			existing[sp.Key] = id
		}
		if first == "" {
			first = id
		}
	}
	if first == "" {
		return "", fmt.Errorf("no resumable sessions to open")
	}
	return first, nil
}

// tagTmuxWindow stores the window's Spec on the window itself (base64 JSON — safe through
// tmux's option quoting), which is what --save-workspace reads back.
func tagTmuxWindow(windowID string, sp ptymux.Spec) {
	b, err := json.Marshal(sp)
	if err != nil {
		return
	}
	_ = tmuxCmd("set-option", "-w", "-t", windowID, "@ptln_key", sp.Key).Run()
	_ = tmuxCmd("set-option", "-w", "-t", windowID, "@ptln_spec", base64.StdEncoding.EncodeToString(b)).Run()
}

// tmuxWorkspaceSpecs reads every tagged window's Spec back off the server — the tmux-side
// equivalent of the built-in mux's liveSpecs().
func tmuxWorkspaceSpecs() []ptymux.Spec {
	out, err := tmuxCmd("list-windows", "-t", tmuxSessionName, "-F", "#{@ptln_spec}").Output()
	if err != nil {
		return nil
	}
	var specs []ptymux.Spec
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			continue
		}
		var sp ptymux.Spec
		if json.Unmarshal(b, &sp) == nil && len(sp.Argv) > 0 && sp.Key != tmuxLauncherKey {
			// the launcher is a fixture, recreated on every open — never part of the workspace
			specs = append(specs, sp)
		}
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
	return tmuxCmd("select-window", "-t", target).Run()
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
