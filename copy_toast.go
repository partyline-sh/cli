package main

// "✓ copied" in the ribbon, at the right, instead of over the tabs.
//
// display-message cannot be positioned: it takes the whole message line, so it either covered the
// tab row (navigation somebody is reading) or the memory row. Neither is a good place for a toast
// that means "the clipboard write worked".
//
// So the confirmation goes through the status renderer, which already owns row 1 and can align to
// the right. The copy binding drops a marker and asks tmux to repaint; the row shows the toast
// while the marker is fresh and stops on its own. Nothing to clear, nothing to time out.

import (
	"os"
	"path/filepath"
	"time"

	"partyline.sh/partyline/internal/brand"
)

// copyToastFor is how long the confirmation stays up. Long enough to register, short enough that
// it is gone before you look back at the ribbon for what it normally says.
const copyToastFor = 1500 * time.Millisecond

func copyToastPath() string { return filepath.Join(stateDir(), "copied") }

// markCopied records the copy and repaints the bar at once — the status interval is a floor, and
// a toast that waits up to five seconds for the next tick is not a toast.
func markCopied() {
	_ = os.WriteFile(copyToastPath(), []byte(time.Now().Format(time.RFC3339Nano)), 0o600)
	pokeStatus()
}

// copyToast is the right-aligned confirmation, or "" once it has aged out.
func copyToast() string {
	fi, err := os.Stat(copyToastPath())
	if err != nil || time.Since(fi.ModTime()) > copyToastFor {
		return ""
	}
	return "#[align=right,fg=" + brand.Hex(brand.AmberRGB) + "]✓ copied — " + pasteChord() + " to paste #[default]"
}
