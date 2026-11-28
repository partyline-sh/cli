package main

import (
	"fmt"
	"os"
	"time"

	"partyline.sh/partyline/internal/remote"
)

// Only a shell pane in Partyline's own session may be closed, and the process
// identity must still match what the phone displayed before its confirmation.
func remoteTerminalClose(phone string, m remote.Message, now time.Time) (string, string, any) {
	if !remoteSessionPane.MatchString(m.Pane) || m.Target == "" || len(m.Target) > 256 || m.Data != nil {
		return "refused", "Invalid terminal close request", nil
	}
	rows, err := remoteSnapshot("sessions", "focus")
	if err != nil {
		return "refused", "Could not verify terminal", nil
	}
	found := false
	for _, row := range rows.([]map[string]string) {
		if row["session"] == tmuxSessionName && row["pane"] == m.Pane && row["terminalTarget"] == m.Target {
			found = true
			break
		}
	}
	if !found {
		return "refused", "Terminal changed or is no longer available; refresh before closing", nil
	}
	if err = remoteControlAudit(phone, "terminal_close", m.Pane, "", "", now); err == nil {
		err = tmuxCmd("kill-pane", "-t", m.Pane).Run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote terminal close failed:", err)
		return "refused", "Could not close terminal; refresh before retrying", nil
	}
	saveWorkspace(tmuxWorkspaceSpecs())
	return "ok", "", map[string]string{"pane": m.Pane}
}
