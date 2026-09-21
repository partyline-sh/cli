package main

// PROTOTYPE — tmux-backed session host (`ptln tmux`).
//
// This is Option 3 from the mux-corruption analysis: instead of the built-in pass-through
// multiplexer (internal/ptymux) owning the terminal, tmux does. tmux keeps a full server-side
// grid per window, so switching between differential-redraw TUIs (claude, codex) can never
// leave stale cells behind — the exact structural defect the pass-through mux cannot fix.
//
// Scope of the prototype:
//   - `ptln tmux`           one window running the quick engine (PARTYLINE_ENGINE or claude)
//   - `ptln tmux --resume`  one window per saved workspace session (same resume argv the
//     built-in mux would use, permission flags included)
//   - branded status bar (wordmark amber, focused-tab pill pink), ctrl-\ prefix to match
//     the built-in mux's chord, n/N new-session keys mirroring the switchboard
//
// NOT in the prototype (deliberately): the launcher/home screen, session sharing, thread
// wiring, boot splash, workspace save-on-quit. Those come if the backend graduates.
//
// Everything runs on a private tmux server (socket "partyline") so the user's own tmux
// sessions are untouched.

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"partyline.sh/partyline/internal/brand"
	"partyline.sh/partyline/internal/ptymux"
)

// tmuxSocketName is the backend's private tmux socket. PARTYLINE_TMUX_SOCKET overrides it —
// the socket lives in /tmp/tmux-<uid>/ (machine-wide per user, NOT per $HOME), so tests and
// scripts MUST override it or they attach to, and can kill, the operator's real server.
func tmuxSocketName() string {
	if s := strings.TrimSpace(os.Getenv("PARTYLINE_TMUX_SOCKET")); s != "" {
		return s
	}
	return "partyline"
}

const tmuxSessionName = "ptln"

func tmuxCmdMain(args []string) {
	// runs INSIDE a popup: the per-session menu (c|m|w|g|n) for the active window — hidden
	if len(args) > 0 && args[0] == "--session-menu" {
		which := ""
		if len(args) > 1 {
			which = args[1]
		}
		tmuxSessionMenu(which)
		return
	}
	// runs as the CHILD of a share Session: stream the pane to stdout — hidden
	if len(args) > 1 && args[0] == "--tap" {
		tmuxTapMain(args[1])
		return
	}
	var resume, printConf, save, newWin, bypass, detached, quit bool
	for _, a := range args {
		switch a {
		case "--resume":
			resume = true
		case "--print-conf":
			printConf = true
		case "--save-workspace": // hook target (window-unlinked) — hidden
			save = true
		case "--detached": // hook target (client-detached): snapshot + scribe — hidden
			save = true
			detached = true
		case "--quit": // shut EVERYTHING down, resumable — the update-friendly exit (menu Q)
			quit = true
		case "--new": // chord target (prefix n / N) — hidden
			newWin = true
		case "--bypass":
			bypass = true
		case "--feed": // ctrl-\ f — show or hide the activity pane
			tmuxFeedToggle()
			return
		case "--session-popup": // hidden; opens a per-session menu AFTER the menu popup has closed
			which := ""
			if i := indexOfArg(args, "--session-popup"); i >= 0 && i+1 < len(args) {
				which = args[i+1]
			}
			if err := tmuxSessionPopup(which); err != nil {
				fatal(fmt.Errorf("ptln tmux --session-popup: %w", err))
			}
			return
		case "--home": // chord target (prefix o) — hidden
			if err := tmuxHome(); err != nil {
				fatal(fmt.Errorf("ptln tmux --home: %w", err))
			}
			return
		case "--status": // status row 1 — hidden; tmux runs it for the active pane's directory
			d := ""
			if i := indexOfArg(args, "--status"); i >= 0 && i+1 < len(args) {
				d = args[i+1]
			}
			if d == "" {
				d, _ = os.Getwd()
			}
			// The toast renders even outside a project, where the memory half is empty: the
			// clipboard worked either way and that is what it is confirming.
			fmt.Print(memoryStatusLine(d) + copyToast())
			return
		case "--copied": // hidden; the copy binding calls this, then the ribbon shows it
			markCopied()
			return
		case "--menu": // chord target (ctrl-\) — hidden; opens the centered popup
			if err := tmuxMenuOpen(); err != nil {
				fatal(fmt.Errorf("ptln tmux --menu: %w", err))
			}
			return
		case "--menu-tui": // runs INSIDE the popup — hidden
			if err := tmuxMenuTUI(); err != nil {
				fatal(fmt.Errorf("ptln tmux --menu-tui: %w", err))
			}
			return
		default:
			fatal(fmt.Errorf("ptln tmux: unknown flag %q", a))
		}
	}

	if printConf {
		fmt.Print(tmuxConf())
		return
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		fatal(fmt.Errorf("ptln tmux: tmux is not installed (brew install tmux / apt install tmux)"))
	}
	if !tmuxUsable() {
		fatal(fmt.Errorf("ptln tmux: needs tmux 3.3 or newer (the generated conf uses popup styling an older tmux rejects)"))
	}
	if save {
		tmuxSaveWorkspace()
		if detached {
			// the human just left — capture each thread-attached session's context before it
			// goes cold, exactly what the built-in mux's BeforeQuit did at quit
			scribeOnQuit(tmuxWorkspaceSpecs())
		}
		return
	}
	if quit {
		tmuxQuitAll()
		return
	}
	if newWin {
		if err := tmuxNewWindow(bypass); err != nil {
			fatal(fmt.Errorf("ptln tmux --new: %w", err))
		}
		return
	}
	if os.Getenv("TMUX") != "" && !insidePtlnTmux() {
		fatal(fmt.Errorf("ptln tmux: already inside another tmux session — detach first (prefix d)"))
	}

	var specs []ptymux.Spec
	if resume {
		specs = loadWorkspace()
		if len(specs) == 0 {
			fmt.Println("no saved workspace — starting a fresh session instead")
		}
	}
	if len(specs) == 0 {
		cwd, _ := os.Getwd()
		spec, err := newSessionSpec(quickNewEngine(), cwd, "", "", "", false, false, 0)
		if err != nil {
			fatal(fmt.Errorf("ptln tmux: %w", err))
		}
		specs = []ptymux.Spec{inheritRepoBindSpec(spec)}
	}
	if err := runTmuxApp(specs); err != nil {
		fatal(fmt.Errorf("ptln tmux: %w", err))
	}
}

// tmuxQuitAll shuts the whole session host down, RESUMABLY — the exit `q` (detach) is not.
//
// WHY IT EXISTS. Detach leaves every engine running inside the private tmux server, which is
// exactly right for "back in an hour" and exactly wrong for updating: claude/codex refuse to
// self-update while instances are live, and a fleet of long-lived engine processes keeps running
// last week's code however many times `ptln update` has replaced the binary on disk. The only
// full stop used to be `tmux -L partyline kill-server` — a socket name nobody should have to know.
//
// Order matters: snapshot FIRST (the save reads the live windows), scribe while the panes'
// transcripts are freshest, kill LAST. After the kill, `ptln tmux --resume` reopens every saved
// session with its engine's native resume — the conversation survives the shutdown, which is the
// entire difference between this and pulling the plug.
//
// This closes EVERYTHING on the server — chat sessions and any crank run windows alike — which is
// why the menu row that reaches here confirms first and says so.
func tmuxQuitAll() {
	if tmuxCmd("has-session", "-t", tmuxSessionName).Run() != nil {
		fmt.Println("nothing to shut down — the partyline session host isn't running")
		return
	}
	tmuxSaveWorkspace()
	scribeOnQuit(tmuxWorkspaceSpecs())
	if err := tmuxCmd("kill-server").Run(); err != nil {
		fatal(fmt.Errorf("ptln tmux --quit: could not stop the session host: %w", err))
	}
	fmt.Println("✓ all sessions closed — workspace saved")
	fmt.Println("  update freely (ptln update, claude, …), then `ptln --resume` brings every session back")
}

// tmuxWindowName keeps labels safe for the status bar: control chars stripped, length capped.
var tmuxUnprintable = regexp.MustCompile(`[[:cntrl:]#]`)

func tmuxWindowName(label string) string {
	s := tmuxUnprintable.ReplaceAllString(label, "")
	s = strings.TrimSpace(s)
	if s == "" {
		s = "session"
	}
	if len([]rune(s)) > 24 {
		s = string([]rune(s)[:24])
	}
	return s
}

// tmuxConf renders the generated config. Brand colors come from the brand package
// (wordmark amber, focused-pill pink) so a palette change there reaches this bar too.
func tmuxConf() string {
	// tmux renders the ribbon and popup borders itself, from this conf — nothing Go writes passes
	// through themed() on the way. So the accent is resolved HERE, and the conf is rewritten on
	// every launch so switching themes takes effect on the next open.
	//
	// Under the identity theme it stays the exact brand hex: colour215 is only NEAR #ff9838, and
	// the default look should not drift because a themable path was added for everyone else.
	amber := brand.Hex(brand.AmberRGB)
	if idx := ThemedIndex(accentIdx); idx != accentIdx {
		amber = "colour" + idx
	}
	pill := brand.Hex(brand.PillRGB)
	var b strings.Builder
	w := func(line string) { b.WriteString(line + "\n") }

	w(`# GENERATED by ptln tmux (prototype backend). Rewritten on every launch — do not edit.`)
	w(``)
	w(`set -g default-terminal "tmux-256color"`)
	w(`set -ga terminal-overrides ",*:RGB"`)
	w(`set -s escape-time 0`)
	// Modified keys must survive the hop into a pane. Shift-Enter, Option-Enter and friends reach
	// an app inside tmux only when tmux is told to negotiate the extended-key protocol on its
	// behalf; without these three the terminal's rich key sequences are flattened on the way in and
	// the app sees a bare Enter, which is why a newline in an agent's prompt box submitted instead.
	// These are the settings Claude Code's own docs require of a tmux host, and every engine we
	// host has the same need. allow-passthrough additionally lets a pane talk to the outer terminal
	// directly (OSC sequences — clipboard, images), which the same apps rely on.
	w(`set -s extended-keys on`)
	w(`set -as terminal-features "xterm*:extkeys"`)
	w(`set -g allow-passthrough on`)
	w(`set -g display-time 6000`)
	w(`set -g history-limit 50000`)
	w(`set -g mouse on`)
	// Mouse mode makes tmux own selection: a drag runs tmux's copy-mode and the release used
	// to strand the text in tmux's internal buffer — "can't copy any more". Pipe the selection
	// to the LOCAL clipboard tool (the tmux server runs on this machine, so pbcopy/wl-copy
	// work no matter what the outer terminal supports); OSC 52 stays on as belt-and-braces
	// for terminals that honor it.
	//
	// AND-CANCEL, NOT NO-CLEAR. no-clear kept the highlight visible after the release, which
	// looked right and left the pane sitting in copy-mode — and a pane in copy-mode routes every
	// keystroke to the scrollback viewer. So the obvious flow, highlight something and paste it
	// into the prompt below, did nothing: the paste went to the viewer. You had to click or press
	// Escape first, with nothing on screen saying why. Releasing the mouse now copies AND leaves
	// copy-mode, which is what Enter below has always done. The cost is that the highlight does
	// not persist after the release; the text is on the clipboard, which is the point of it.
	//
	// Cancel on RELEASE is safe. The bug that produced the first version of this was cancel on
	// PRESS — every drag begins with a press, so it snapped the view back to live the instant
	// anyone tried to select in scrollback.
	//
	// AND A MESSAGE, because losing the highlight reads as "it did not work". The highlight was
	// doing double duty as confirmation, and doing it badly: it looked identical whether or not
	// the clipboard write actually succeeded. Say so instead.
	w(`set -g set-clipboard on`)
	if clip := clipboardCmd(); clip != "" {
		copied := ` \; run-shell -b ` + shQuote(selfExe()+" tmux --copied")
		w(`bind -T copy-mode MouseDragEnd1Pane send -X copy-pipe-and-cancel ` + shQuote(clip) + copied)
		w(`bind -T copy-mode-vi MouseDragEnd1Pane send -X copy-pipe-and-cancel ` + shQuote(clip) + copied)
		w(`bind -T copy-mode Enter send -X copy-pipe-and-cancel ` + shQuote(clip))
		w(`bind -T copy-mode-vi Enter send -X copy-pipe-and-cancel ` + shQuote(clip))
	} else {
		copied := ` \; run-shell -b ` + shQuote(selfExe()+" tmux --copied")
		w(`bind -T copy-mode MouseDragEnd1Pane send -X copy-selection-and-cancel` + copied)
		w(`bind -T copy-mode-vi MouseDragEnd1Pane send -X copy-selection-and-cancel` + copied)
	}
	// A click clears a lingering selection — but it must NOT cancel copy-mode: every drag
	// STARTS with this same press, and cancel-on-press snapped the view back to live the
	// moment anyone tried to select in scrollback (the field report that replaced the first
	// version of this binding). Clear only; the ways back to live are wheel-to-bottom (the
	// -e on copy-mode below), esc, or q.
	w(`bind -T copy-mode MouseDown1Pane { select-pane; send -X clear-selection }`)
	w(`bind -T copy-mode-vi MouseDown1Pane { select-pane; send -X clear-selection }`)
	// Wheel-up is tmux scrollback, uniformly. Left to the default, an app that enabled the
	// mouse (claude does) swallows the wheel into its OWN view — which you cannot select
	// from, and which snaps to the tail on new output. Full-screen apps (alternate_on: vim,
	// htop) keep their wheel; everything else scrolls tmux history, where selection and the
	// clipboard pipe work, and scrolling back to the bottom drops you live again (-e).
	w(`bind -n WheelUpPane if -F '#{?#{alternate_on},#{mouse_any_flag},0}' { send -M } { copy-mode -e; send -M }`)
	// Any PRINTABLE key in copy-mode cancels the mode and TYPES — into the session's input,
	// where the keystroke was aimed. Copy-mode is entered by a wheel or a drag; when fingers
	// go back to the keyboard the human has moved on, and a modality that eats their first
	// word (recoverable only by knowing to press esc) fails them at the exact moment they
	// stopped thinking about it. Enter still copies; esc still just exits; arrows still
	// navigate. The backslash key is skipped — its tmux-conf quoting is a hazard chain not
	// worth one key that still types fine after any other key exits the mode.
	w(`# ---- copy-mode: printable keys cancel-and-type (see the note in tmux_backend.go) ----`)
	for _, table := range []string{"copy-mode", "copy-mode-vi"} {
		for c := byte(33); c <= 126; c++ {
			if c == '\\' {
				continue
			}
			key := "'" + string(c) + "'"
			if c == '\'' {
				key = `"'"`
			}
			payload := shQuote(string(c))
			if c == '\'' {
				payload = `"'"`
			}
			// Order is LOAD-BEARING, both ways. Cancel must come first: send-keys re-enters
			// the key tables, so typing while still in copy-mode re-triggers this very
			// binding — an infinite loop that pegged a tmux server at 100% CPU in testing.
			// And cancel must be GUARDED: two fast keystrokes both arrive through this
			// table, and a bare cancel on the second errors and aborts the block, eating
			// the character. Guarded-cancel-then-type survives both.
			w(`bind -T ` + table + ` ` + key + ` { if -F '#{pane_in_mode}' { send -X cancel }; send-keys -l -- ` + payload + ` }`)
		}
		w(`bind -T ` + table + ` Space { if -F '#{pane_in_mode}' { send -X cancel }; send-keys -l -- ' ' }`)
		w(`bind -T ` + table + ` BSpace { if -F '#{pane_in_mode}' { send -X cancel }; send-keys BSpace }`)
	}
	w(`set -g base-index 1`)
	w(`setw -g pane-base-index 1`)
	w(`set -g renumber-windows on`)
	w(`setw -g aggressive-resize on`)
	w(`set -g set-titles on`)
	w(`set -g set-titles-string "ptln · #W"`)
	w(``)
	w(`# ---- one control surface: ctrl-\ opens THE menu (sessions + commands, hotkeys shown) ----`)
	w(`set -g prefix None`)
	w("bind -n 'C-\\' run-shell " + shQuote(selfExe()+" tmux --menu"))
	for i := 1; i <= 9; i++ {
		w(fmt.Sprintf(`bind -n M-%d select-window -t %d`, i, i))
	}
	// The memory watcher rides the tmux server: started in the background, self-limited to one
	// per machine by its own lock, so re-sourcing this conf on every launch cannot pile them up.
	w(`run-shell -b ` + shQuote(selfExe()+" memory watch"))
	w(``)
	w(`# ---- workspace snapshot: --resume reflects the last detach, like the mux quit hook ----`)
	w(`set-hook -g client-detached ` + shQuote("run-shell "+shQuote(selfExe()+" tmux --detached")))
	w(`set-hook -g window-unlinked ` + shQuote("run-shell "+shQuote(selfExe()+" tmux --save-workspace")))
	w(``)
	w(`# ---- status bar (partyline brand) ----`)
	// TWO ROWS. The tabs keep row 0; row 1 is partyline's own state — project, what the team has
	// learned, disagreements, anything not yet shared. Everything this system does was otherwise
	// invisible to the human until an agent mentioned it mid-session.
	w(`set -g status 2`)
	w(`set -g status-format[1] ` + shQuote("#[bg=#101010]#("+selfExe()+" tmux --status '#{pane_current_path}')"))
	// NO `set -g status on` HERE. In tmux, `status` is the ROW COUNT and `on` means exactly one
	// row — so writing it after `status 2` silently collapsed the bar back to a single row. The
	// second row was configured, never drawn, and the whole memory ribbon looked like it had
	// simply not shipped. `status 2` above already turns the bar on.
	//
	// The interval is a FLOOR, not the update rate: a local write pokes the bar immediately
	// (refresh-client -S) and the watcher pokes it when a teammate's facts land, so this only
	// covers the case where both miss.
	w(`set -g status-position bottom`)
	w(`set -g status-interval 5`)
	w(`set -g status-style "bg=#101010,fg=#8a8a8a"`)
	w(`set -g status-left "#[fg=` + amber + `,bold] ☎ PARTYLINE #[default]"`)
	w(`set -g status-left-length 24`)
	w(`# the menu's front door lives ON the ribbon — nobody should have to guess the chord`)
	w(`set -g status-right "#[fg=#8a8a8a]menu #[fg=` + amber + `,bold]ctrl-\\#[default]#[fg=#8a8a8a] · %H:%M "`)
	w(`set -g status-right-length 48`)
	w(`setw -g window-status-separator ""`)
	w(`# names truncate on the ribbon so a dozen sessions stay readable — full names in the menu`)
	w(`setw -g window-status-format "#[fg=#8a8a8a] #{?@ptln_shared,⇡ ,}#I·#{=/14/…:window_name} "`)
	w(`setw -g window-status-current-format "#[bg=` + pill + `,fg=#101010,bold] #{?@ptln_shared,⇡ ,}#I·#{=/14/…:window_name} #[default]"`)
	w(`setw -g window-status-activity-style "fg=` + amber + `"`)
	w(`set -g message-style "bg=#101010,fg=` + amber + `"`)
	w(`set -g mode-style "bg=` + amber + `,fg=#101010"`)
	w(`# the menu wears the same clothes as every ptln modal: amber rounded frame, ink ground,`)
	w(`# pill-pink selection — one visual language, not tmux's default costume over ptln's bar.`)
	w(`set -g popup-style "bg=#101010,fg=#8a8a8a"`)
	w(`set -g popup-border-style "fg=` + amber + `,bg=#101010"`)
	w(`set -g popup-border-lines rounded`)
	w(`set -g pane-border-style "fg=#3a3a3a"`)
	w(`set -g pane-active-border-style "fg=` + amber + `"`)
	return b.String()
}

// clipboardCmd finds the local system-clipboard writer for copy-pipe: pbcopy on macOS,
// wl-copy/xclip/xsel on Linux. "" means none found — the conf falls back to OSC 52 and the
// tmux buffer, which is all that's possible there.
func clipboardCmd() string {
	candidates := []string{"pbcopy", "wl-copy", "xclip", "xsel"}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			switch c {
			case "xclip":
				return "xclip -in -selection clipboard"
			case "xsel":
				return "xsel --input --clipboard"
			}
			return c
		}
	}
	return ""
}

// indexOfArg finds a flag's position so its value can be read positionally — the tmux status
// command passes the directory as a bare argument after the flag.
func indexOfArg(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// pasteChord is the key the person actually presses to paste, which is the terminal's business
// rather than tmux's: on macOS it is ⌘V, everywhere else ctrl-shift-V. Naming the wrong one in a
// confirmation is worse than naming none.
func pasteChord() string {
	if runtime.GOOS == "darwin" {
		return "⌘V"
	}
	return "ctrl-shift-V"
}
