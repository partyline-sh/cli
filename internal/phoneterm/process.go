// Package phoneterm owns detached phone shells on a dedicated tmux server.
// The terminal protocol is supplied by an ordinary tmux client on a real PTY.
package phoneterm

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
)

const Prefix = "ptln-phone-"

func dimensions(cols, rows int) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(min(max(cols, 20), 300)), Rows: uint16(min(max(rows, 8), 200))}
}

func environment() []string {
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "TMUX", "TMUX_PANE", "TERM", "COLUMNS", "LINES":
			continue
		}
		env = append(env, value)
	}
	return append(env, "TERM=xterm-256color")
}

func command(socket string, args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", append([]string{"-L", socket, "-f", os.DevNull}, args...)...)
	cmd.Env = environment()
	return cmd
}

// Attachment has one reader and one writer. Call Wait only after draining Read;
// Close unblocks both sides and detaches the tmux client without ending the shell.
type Attachment struct {
	Master *os.File
	cmd    *exec.Cmd
	once   sync.Once
}

func attach(socket, session string, cols, rows int) (*Attachment, error) {
	cmd := command(socket, "attach-session", "-t", session)
	master, err := pty.StartWithSize(cmd, dimensions(cols, rows))
	if err != nil {
		return nil, fmt.Errorf("attach phone terminal: %w", err)
	}
	return &Attachment{Master: master, cmd: cmd}, nil
}

func (a *Attachment) Resize(cols, rows int) error {
	return pty.Setsize(a.Master, dimensions(cols, rows))
}
func (a *Attachment) Close() {
	a.once.Do(func() {
		_ = a.Master.Close()
		if a.cmd.Process != nil {
			_ = a.cmd.Process.Kill()
		}
	})
}
func (a *Attachment) Wait() error { return a.cmd.Wait() }
