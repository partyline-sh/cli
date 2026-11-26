package main

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

type setupRenderBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *setupRenderBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *setupRenderBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func TestSetupRunsInRealTerminalRendererWithoutApplyingConfiguration(t *testing.T) {
	var output setupRenderBuffer
	defer func() {
		_ = os.WriteFile("/tmp/partyline-private-gateway-terminal-render.txt", []byte(xansi.Strip(output.String())), 0600)
	}()
	p := tea.NewProgram(newRelaySetupModel(), tea.WithInput(nil), tea.WithOutput(&output), tea.WithWindowSize(80, 24), tea.WithoutSignalHandler())
	type result struct {
		model tea.Model
		err   error
	}
	done := make(chan result, 1)
	go func() { m, err := p.Run(); done <- result{m, err} }()
	defer p.Kill()
	p.Send(tea.KeyPressMsg{Code: tea.KeyDown})
	p.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !strings.Contains(strings.ToLower(xansi.Strip(output.String())), "name your private gateway") {
		select {
		case <-deadline:
			t.Fatal("terminal renderer never showed the selected setup screen")
		case r := <-done:
			t.Fatal("setup exited before rendering", r.err)
		case <-ticker.C:
		}
	}
	p.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.model.(relaySetupModel).action.Kind != "" {
			t.Fatal("cancellation applied configuration")
		}
		_ = os.WriteFile("/tmp/partyline-private-gateway-terminal-render.txt", []byte(xansi.Strip(output.String())), 0600)
	case <-time.After(3 * time.Second):
		t.Fatal("setup did not close after cancellation")
	}
}
