package main

import (
	"fmt"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The popup's terminal escape is not equivalent to a session-pane copy: tmux
// owns the outer SSH client. Ask tmux to store and deliver the selection itself.
func chatCopyText(text string) tea.Cmd {
	lines := strings.Count(text, "\n") + 1
	if insidePtlnTmux() {
		return func() tea.Msg {
			cmd := tmuxCmd("load-buffer", "-w", "-")
			cmd.Stdin = strings.NewReader(text)
			if err := cmd.Run(); err != nil {
				return chatCopied{lines: lines, err: fmt.Errorf("tmux copy: %w", err)}
			}
			markCopied()
			return chatCopied{lines: lines, tmux: true}
		}
	}
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		ok, err := chatClipboardWrite(text)
		return chatCopied{lines: lines, native: ok, err: err}
	})
}

// Outside Partyline tmux, retain the local clipboard writer and terminal fallback.
var chatClipboardWrite = func(text string) (bool, error) {
	writer := clipboardCmd()
	if writer == "" {
		return false, nil
	}
	cmd := exec.Command("sh", "-c", writer)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("%s: %w", strings.Fields(writer)[0], err)
	}
	return true, nil
}
