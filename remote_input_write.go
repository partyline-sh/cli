package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"partyline.sh/partyline/internal/remote"
	"strings"
	"time"
)

func runRemoteInput(m remote.Message) error {
	identity, err := bindRemoteInput(m)
	if err != nil {
		return err
	}
	data := m.Data.(string)
	if m.Kind == "key" {
		return tmuxCmd("send-keys", "-t", m.Pane, "--", data).Run()
	}
	if err := insertRemoteInput(m); err != nil {
		return err
	}
	if m.Kind == "text" {
		// Agent composers suppress Enter while processing a paste burst.
		time.Sleep(300 * time.Millisecond)
		current, err := remotePaneIdentity(m.Pane)
		if err != nil || current != identity {
			return errors.New("agent changed or exited; no Enter sent")
		}
		return tmuxCmd("send-keys", "-t", m.Pane, "--", "Enter").Run()
	}
	return nil
}

func insertRemoteInput(m remote.Message) error {
	data := m.Data.(string)
	if m.Kind == "paste" || strings.Contains(data, "\n") {
		// Unique buffer names avoid interfering with the person's clipboard.
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		name := "ptln-remote-" + hex.EncodeToString(nonce[:])
		load := tmuxCmd("load-buffer", "-b", name, "--", "-")
		load.Stdin = strings.NewReader(data)
		if err := load.Run(); err != nil {
			return err
		}
		defer func() { _ = tmuxCmd("delete-buffer", "-b", name).Run() }()
		if err := tmuxCmd("paste-buffer", "-p", "-d", "-b", name, "-t", m.Pane).Run(); err != nil {
			return err
		}
	} else if err := tmuxCmd("send-keys", "-t", m.Pane, "-l", "--", remoteTmuxLiteral(data)).Run(); err != nil {
		return err
	}
	return nil
}

// tmux's argv parser treats a trailing semicolon as a command separator even
// after --. Escape that separator for tmux (there is no shell to escape for).
func remoteTmuxLiteral(data string) string {
	if strings.HasSuffix(data, ";") {
		return data[:len(data)-1] + "\\;"
	}
	return data
}
