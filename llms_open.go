// Opening a session in a NEW terminal tab from the `ptln llms` menu.
//
// The menu stays open; the new tab runs `ptln llms resume <id>`, which cd's
// to the session's recorded cwd and execs the tool's native resume. There's no
// portable "open a tab" primitive, so this is a detection ladder over the
// terminals we can drive — ordered most-specific first (multiplexer, then
// terminals that set identifying env vars, then desktop-Linux fallbacks) — and
// an honest error elsewhere. Where a terminal can't script a *tab*, we open a
// window rather than fake keystrokes through accessibility APIs.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// openInNewTab launches `ptln llms resume <id>` in a new tab/window of the
// hosting terminal. Returns a short status string for the menu's flash line.
func openInNewTab(s aiSession) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = "partyline"
	}
	cmd := fmt.Sprintf("%s llms resume %s", shQuote(exe), s.ID)

	switch {
	case os.Getenv("TMUX") != "":
		// Inside tmux a "tab" is a window. Most reliable of all; covers SSH too.
		if err := exec.Command("tmux", "new-window", "-n", s.Tool, cmd).Run(); err != nil {
			return "", fmt.Errorf("tmux new-window: %w", err)
		}
		return "opened in a tmux window", nil

	case os.Getenv("WEZTERM_PANE") != "":
		// WezTerm's CLI talks to the running instance; spawn = new tab.
		if err := exec.Command("wezterm", "cli", "spawn", "--", "sh", "-c", cmd).Run(); err != nil {
			return "", fmt.Errorf("wezterm cli spawn: %w", err)
		}
		return "opened in a new WezTerm tab", nil

	case os.Getenv("KITTY_WINDOW_ID") != "":
		// kitty remote control; must be enabled in kitty.conf.
		if err := exec.Command("kitten", "@", "launch", "--type=tab", "sh", "-c", cmd).Run(); err != nil {
			return "", fmt.Errorf("kitty remote control failed — set `allow_remote_control yes` in kitty.conf (%w)", err)
		}
		return "opened in a new kitty tab", nil

	case os.Getenv("TERM_PROGRAM") == "iTerm.app":
		// iTerm2 has a first-class tab API.
		script := fmt.Sprintf(`tell application "iTerm2"
  tell current window to set t to (create tab with default profile)
  tell current session of t to write text %s
end tell`, asQuote(cmd))
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			return "", fmt.Errorf("iTerm2 scripting failed (System Settings → Privacy → Automation?): %w", err)
		}
		return "opened in a new iTerm2 tab", nil

	case os.Getenv("TERM_PROGRAM") == "Apple_Terminal":
		// Terminal.app's `do script` opens a window, not a tab — real tabs need
		// synthetic cmd-T keystrokes via accessibility permissions, which fail
		// closed and confuse. A window is the dependable version of the same idea.
		script := fmt.Sprintf(`tell application "Terminal"
  activate
  do script %s
end tell`, asQuote(cmd))
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			return "", fmt.Errorf("Terminal scripting failed: %w", err)
		}
		return "opened in a new Terminal window", nil

	case os.Getenv("TERM_PROGRAM") == "ghostty":
		// Ghostty has no remote-control API yet; a fresh window is the best we
		// can script. On macOS go through `open` so it reuses the running app.
		if runtime.GOOS == "darwin" {
			if err := exec.Command("open", "-na", "Ghostty", "--args", "-e", "sh", "-c", cmd).Run(); err != nil {
				return "", fmt.Errorf("ghostty open: %w", err)
			}
		} else {
			if err := exec.Command("ghostty", "-e", "sh", "-c", cmd).Start(); err != nil {
				return "", fmt.Errorf("ghostty: %w", err)
			}
		}
		return "opened in a new Ghostty window", nil

	case os.Getenv("ALACRITTY_SOCKET") != "" || os.Getenv("ALACRITTY_WINDOW_ID") != "":
		// Alacritty has no tabs by design; msg create-window reuses the instance.
		if err := exec.Command("alacritty", "msg", "create-window", "-e", "sh", "-c", cmd).Run(); err != nil {
			return "", fmt.Errorf("alacritty msg: %w", err)
		}
		return "opened in a new Alacritty window", nil

	case os.Getenv("GNOME_TERMINAL_SCREEN") != "":
		if err := exec.Command("gnome-terminal", "--tab", "--", "sh", "-c", cmd).Run(); err != nil {
			return "", fmt.Errorf("gnome-terminal: %w", err)
		}
		return "opened in a new GNOME Terminal tab", nil

	case os.Getenv("KONSOLE_VERSION") != "":
		if err := exec.Command("konsole", "--new-tab", "-e", "sh", "-c", cmd).Run(); err != nil {
			return "", fmt.Errorf("konsole: %w", err)
		}
		return "opened in a new Konsole tab", nil
	}

	// Last resort on Linux desktops: the distro's configured default terminal
	// (Debian alternatives). Opens a window; better than refusing.
	if runtime.GOOS == "linux" {
		if xte, err := exec.LookPath("x-terminal-emulator"); err == nil {
			if err := exec.Command(xte, "-e", "sh", "-c", cmd).Start(); err == nil {
				return "opened in a new terminal window", nil
			}
		}
	}
	return "", fmt.Errorf("new-tab not supported in this terminal — works in tmux, iTerm2, Terminal.app, WezTerm, kitty, Ghostty, Alacritty, GNOME Terminal, Konsole")
}

// shQuote single-quotes a string for sh (the resume id/path may carry spaces).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// asQuote double-quotes a string as an AppleScript literal.
func asQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
