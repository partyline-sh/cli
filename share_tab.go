package main

import (
	"fmt"
	"os"
	"os/exec"
)

// openSharedTerminalTab opens a NEW terminal tab/window running `ptln start` — a fresh shared
// shell (its own join link prints in that tab), so you can pull someone onto a live terminal
// without disturbing the mux you're in. macOS + iTerm2/Apple Terminal are automated via
// osascript; anything else gets a copy-paste instruction. Returns a one-line status for the
// mux banner.
func openSharedTerminalTab() string {
	exe := selfExe()
	dir, _ := os.Getwd()
	shellCmd := fmt.Sprintf("cd %s && %s start", shQuote(dir), shQuote(exe))

	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app":
		script := fmt.Sprintf(`tell application "iTerm"
  tell current window
    create tab with default profile
    tell current session to write text %s
  end tell
end tell`, asQuote(shellCmd))
		if err := runOsascript(script); err != nil {
			return "✗ couldn't open an iTerm tab — run `ptln start` in a new tab yourself"
		}
		return "✓ opened a shared terminal in a new iTerm tab (its join link is printing there)"
	case "Apple_Terminal":
		script := fmt.Sprintf(`tell application "Terminal"
  activate
  do script %s
end tell`, asQuote(shellCmd))
		if err := runOsascript(script); err != nil {
			return "✗ couldn't open a Terminal window — run `ptln start` in a new tab yourself"
		}
		return "✓ opened a shared terminal in a new Terminal window (its join link is printing there)"
	default:
		return "open a new tab in your terminal and run `ptln start` to share a shell"
	}
}

func runOsascript(script string) error {
	return exec.Command("osascript", "-e", script).Run()
}
