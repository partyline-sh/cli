package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

const chatDockMarker = "@ptln_chat_dock"
const chatDockBaseLayout = "@ptln_chat_base_layout"

var chatDockPaneID = regexp.MustCompile(`^%[0-9]+$`)

func chatDock() error {
	_, err := dockChatPane(uiPaneTarget(), []string{selfExe(), "chat", "--dock-pane"})
	return err
}

// Docking is opt-in and temporary. Keep the pre-dock agent layout for workspace
// restore; a chat view is not an agent session and must not change session identity.
func dockChatPane(pane string, argv []string) (string, error) {
	if !chatDockPaneID.MatchString(pane) || len(argv) == 0 {
		return "", errors.New("dock chat from a Partyline pane")
	}
	f, err := os.OpenFile(filepath.Join(stateDir(), "chat-dock.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err = os.MkdirAll(relayDir(), 0o700); err != nil {
		return "", err
	}
	var result string
	err = withPhoneLock(func() error {
		var inner error
		result, inner = dockChatPaneLocked(pane, argv)
		return inner
	})
	return result, err
}

func dockChatPaneLocked(pane string, argv []string) (string, error) {
	b, err := tmuxCmd("display-message", "-p", "-t", pane, "#{session_name}\t#{pane_width}\t#{pane_height}\t#{window_id}\t#{window_layout}\t#{"+remoteResizeRestore+"}\t#{"+chatDockBaseLayout+"}").Output()
	parts := strings.Split(strings.TrimRight(string(b), "\r\n"), "\t")
	if err != nil || len(parts) != 7 || parts[0] != tmuxSessionName {
		return "", errors.New("dock chat from a Partyline pane")
	}
	if parts[5] != "" {
		return "", errors.New("turn off Phone layout before docking chat")
	}
	window := parts[3]
	b, err = tmuxCmd("list-panes", "-t", window, "-F", "#{pane_id}\t#{"+chatDockMarker+"}\t#{pane_index}").Output()
	if err != nil {
		return "", err
	}
	lastPane, lastIndex := pane, -1
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) >= 3 {
			if index, e := strconv.Atoi(fields[2]); e == nil && index > lastIndex {
				lastPane, lastIndex = fields[0], index
			}
		}
		if len(fields) >= 2 && fields[1] == "1" {
			return fields[0], tmuxCmd("select-pane", "-t", fields[0]).Run()
		}
	}
	w, _ := strconv.Atoi(parts[1])
	h, _ := strconv.Atoi(parts[2])
	if w < 96 || h < 12 {
		return "", errors.New("docking needs a pane at least 96 columns wide and 12 rows high; use the chat popup here")
	}
	if err = tmuxCmd("set-option", "-w", "-t", window, chatDockBaseLayout, parts[4]).Run(); err != nil {
		return "", err
	}
	// Append after the last pane index and split the full window: existing agent
	// pane numbers stay stable when chat becomes a right-hand sidebar.
	args := []string{"split-window", "-h", "-f", "-l", "38%", "-t", lastPane, "-P", "-F", "#{pane_id}", "-e", "PARTYLINE_UI_PANE=", "--"}
	args = append(args, argv...)
	b, err = tmuxCmd(args...).CombinedOutput()
	if err != nil {
		restoreDockBase(window, parts[6])
		return "", fmt.Errorf("dock chat: %s", strings.TrimSpace(string(b)))
	}
	created := strings.TrimSpace(string(b))
	if !chatDockPaneID.MatchString(created) {
		restoreDockBase(window, parts[6])
		return "", errors.New("tmux did not identify the new chat pane")
	}
	if err = tmuxCmd("set-option", "-p", "-t", created, chatDockMarker, "1").Run(); err != nil {
		_ = tmuxCmd("kill-pane", "-t", created).Run()
		restoreDockBase(window, parts[6])
		return "", err
	}
	return created, nil
}
func restoreDockBase(window, old string) {
	if old == "" {
		_ = tmuxCmd("set-option", "-wu", "-t", window, chatDockBaseLayout).Run()
		return
	}
	_ = tmuxCmd("set-option", "-w", "-t", window, chatDockBaseLayout, old).Run()
}
