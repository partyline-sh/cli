// Package terminalstream carries ordered terminal bytes independently of screen snapshots.
package terminalstream

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"partyline.sh/partyline/internal/phoneterm"
	"partyline.sh/partyline/internal/remote"
)

const Chunk = 16 * 1024
const Window = 256 * 1024
const BatchDelay = 8 * time.Millisecond

type Event struct {
	Owner   string
	Message remote.Message
}
type request struct {
	owner   string
	message remote.Message
}

type Manager struct {
	Registry  phoneterm.Registry
	Events    chan Event
	requests  chan request
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	wg        sync.WaitGroup
	active    map[string]*attachment
	audit     func(owner, id, action string) error
	authorize func(string) bool
}

type attachment struct {
	owner, id, epoch               string
	pty                            *phoneterm.Attachment
	ctx                            context.Context
	cancel                         context.CancelFunc
	mu                             sync.Mutex
	input, accepted, output, acked uint64
	sizes                          map[uint64]int
	outstanding                    int
	credit                         chan struct{}
	writes                         chan input
}
type input struct {
	seq   uint64
	bytes []byte
}

func New(registry phoneterm.Registry, audit func(string, string, string) error, authorize ...func(string) bool) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{Registry: registry, Events: make(chan Event, 32), requests: make(chan request, 64), ctx: ctx, cancel: cancel, active: map[string]*attachment{}, audit: audit}
	if len(authorize) > 0 {
		m.authorize = authorize[0]
	}
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.run() }()
	return m
}
func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	for _, a := range m.active {
		a.cancel()
		a.pty.Close()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
func (m *Manager) Submit(owner string, message remote.Message) error {
	select {
	case <-m.ctx.Done():
		return errors.New("terminal connection closed")
	case m.requests <- request{owner, message}:
		return nil
	default:
		return errors.New("terminal request queue full")
	}
}
func (m *Manager) Drop(owner string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.active {
		if a.owner == owner {
			a.cancel()
			a.pty.Close()
		}
	}
}
func (m *Manager) emit(owner string, message remote.Message) {
	select {
	case m.Events <- Event{owner, message}:
	case <-m.ctx.Done():
	}
}
func (m *Manager) run() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			_ = m.Registry.Cleanup(time.Now())
		case r := <-m.requests:
			m.handle(r)
		}
	}
}
func (m *Manager) handle(r request) {
	if m.ctx.Err() != nil {
		return
	}
	msg := r.message
	if m.authorize != nil && !m.authorize(r.owner) {
		m.Drop(r.owner)
		m.emit(r.owner, remote.Message{Type: "term_result", ID: msg.ID, Pane: msg.Pane, Status: "refused", Reason: "Phone is no longer paired"})
		return
	}
	result := remote.Message{Type: "term_result", ID: msg.ID, Pane: msg.Pane, Status: "ok"}
	var err error
	switch msg.Type {
	case "term_open":
		err = m.open(r.owner, msg, &result)
	case "term_list":
		err = m.Registry.Cleanup(time.Now())
		if err == nil {
			result.Data, err = m.Registry.List(r.owner)
		}
	case "term_close":
		m.mu.Lock()
		a := m.active[msg.Pane]
		if a != nil && a.owner == r.owner {
			a.cancel()
			a.pty.Close()
		}
		m.mu.Unlock()
		err = m.Registry.End(r.owner, msg.Pane)
		if err == nil && m.audit != nil {
			err = m.audit(r.owner, msg.Pane, "close")
		}
	default:
		m.mu.Lock()
		a := m.active[msg.Pane]
		m.mu.Unlock()
		if a == nil || a.owner != r.owner || a.epoch != msg.ID {
			err = errors.New("terminal attachment expired")
			break
		}
		switch msg.Type {
		case "term_detach":
			a.cancel()
			a.pty.Close()
		case "term_resize":
			var size struct {
				Columns int `json:"columns"`
				Rows    int `json:"rows"`
			}
			err = decode(msg.Data, &size)
			if err == nil {
				err = a.pty.Resize(size.Columns, size.Rows)
			}
		case "term_ack":
			err = a.ack(msg.Revision)
		case "term_input":
			a.mu.Lock()
			accepted := a.accepted
			a.mu.Unlock()
			if msg.Revision > 0 && msg.Revision <= accepted {
				m.emit(a.owner, remote.Message{Type: "term_input_ack", ID: a.epoch, Pane: a.id, Revision: accepted})
				return
			}
			err = a.queue(msg)
		default:
			err = errors.New("unsupported terminal operation")
		}
		if err != nil {
			a.cancel()
			a.pty.Close()
		}
		// Input ACK is emitted after the actual PTY write; output ACKs need no response.
		if err == nil && (msg.Type == "term_input" || msg.Type == "term_ack") {
			return
		}
	}
	if err != nil {
		result.Status = "refused"
		result.Reason = err.Error()
	}
	if result.Type != "" {
		m.emit(r.owner, result)
	}
}
func decode(data any, target any) error {
	b, e := json.Marshal(data)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, target)
}

func (m *Manager) open(owner string, msg remote.Message, result *remote.Message) error {
	var config struct {
		Session string `json:"session"`
		Columns int    `json:"columns"`
		Rows    int    `json:"rows"`
	}
	if err := decode(msg.Data, &config); err != nil {
		return err
	}
	if config.Columns < 20 || config.Columns > 300 || config.Rows < 8 || config.Rows > 200 {
		return errors.New("unsupported terminal dimensions")
	}
	if err := m.Registry.Cleanup(time.Now()); err != nil {
		return err
	}
	action := "reattach"
	if config.Session == "" {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		s, err := m.Registry.Create(owner, msg.ID, shell, config.Columns, config.Rows, func(s phoneterm.Session) error {
			if m.audit != nil {
				return m.audit(owner, s.ID, "open")
			}
			return nil
		})
		if err != nil {
			return err
		}
		config.Session = s.ID
		if s.Fresh {
			action = ""
		}
	}
	m.mu.Lock()
	old := m.active[config.Session]
	m.mu.Unlock()
	if old != nil {
		return errors.New("terminal already attached; detach it before reconnecting")
	}
	if m.audit != nil && action != "" {
		if err := m.audit(owner, config.Session, action); err != nil {
			return err
		}
	}
	p, err := m.Registry.Attach(owner, config.Session, config.Columns, config.Rows)
	if err != nil {
		return err
	}
	var nonce [24]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		p.Close()
		_ = p.Wait()
		return err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	a := &attachment{owner: owner, id: config.Session, epoch: hex.EncodeToString(nonce[:]), pty: p, ctx: ctx, cancel: cancel, sizes: map[uint64]int{}, credit: make(chan struct{}, 1), writes: make(chan input, 32)}
	m.mu.Lock()
	m.active[a.id] = a
	m.mu.Unlock()
	result.Data = map[string]any{"session": a.id, "attachment": a.epoch}
	result.Pane = a.id
	// The open result must precede any output for the new epoch.
	m.emit(owner, *result)
	m.wg.Add(2)
	go func() { defer m.wg.Done(); m.read(a) }()
	go func() { defer m.wg.Done(); m.write(a) }()
	result.Type = "" // the caller must not send a duplicate open result
	return nil
}
func (a *attachment) queue(msg remote.Message) error {
	text, ok := msg.Data.(string)
	if !ok {
		return errors.New("invalid terminal input")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil || len(raw) == 0 || len(raw) > 8192 {
		return errors.New("invalid terminal input size or encoding")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if msg.Revision == 0 {
		return errors.New("invalid terminal input sequence")
	}
	if msg.Revision <= a.input {
		return nil
	}
	if msg.Revision != a.input+1 || msg.Revision > 1<<53-1 {
		return errors.New("terminal input sequence gap")
	}
	select {
	case a.writes <- input{msg.Revision, raw}:
		a.input = msg.Revision
		return nil
	default:
		return errors.New("terminal input queue full")
	}
}
func (a *attachment) ack(seq uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if seq > a.output {
		return errors.New("invalid terminal output acknowledgement")
	}
	if seq <= a.acked {
		return nil
	}
	for n := a.acked + 1; n <= seq; n++ {
		a.outstanding -= a.sizes[n]
		delete(a.sizes, n)
	}
	a.acked = seq
	select {
	case a.credit <- struct{}{}:
	default:
	}
	return nil
}
func (m *Manager) write(a *attachment) {
	for {
		select {
		case <-a.ctx.Done():
			return
		case in := <-a.writes:
			for len(in.bytes) > 0 {
				n, err := a.pty.Master.Write(in.bytes)
				if err != nil {
					a.cancel()
					a.pty.Close()
					return
				}
				if n == 0 {
					a.cancel()
					a.pty.Close()
					return
				}
				in.bytes = in.bytes[n:]
			}
			a.mu.Lock()
			a.accepted = in.seq
			a.mu.Unlock()
			m.emit(a.owner, remote.Message{Type: "term_input_ack", ID: a.epoch, Pane: a.id, Revision: in.seq})
		}
	}
}
func (m *Manager) read(a *attachment) {
	chunks := make(chan []byte, 2)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(chunks)
		defer a.pty.Wait()
		buf := make([]byte, 4096)
		for {
			n, err := a.pty.Master.Read(buf)
			if n > 0 {
				copy := append([]byte(nil), buf[:n]...)
				select {
				case chunks <- copy:
				case <-a.ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		a.cancel()
		a.pty.Close()
		m.mu.Lock()
		if m.active[a.id] == a {
			delete(m.active, a.id)
		}
		m.mu.Unlock()
		_ = m.Registry.Detached(a.owner, a.id)
	}()
	var pending []byte
	for {
		if len(pending) == 0 {
			select {
			case <-a.ctx.Done():
				return
			case raw, ok := <-chunks:
				if !ok {
					m.emit(a.owner, remote.Message{Type: "term_exit", ID: a.epoch, Pane: a.id, Reason: "terminal process ended"})
					return
				}
				pending = raw
			}
		}
		timer := time.NewTimer(BatchDelay)
		quiet := time.NewTimer(time.Millisecond)
	collect:
		for len(pending) < Chunk {
			select {
			case <-a.ctx.Done():
				timer.Stop()
				quiet.Stop()
				return
			case <-timer.C:
				break collect
			case <-quiet.C:
				break collect
			case raw, ok := <-chunks:
				if !ok {
					break collect
				}
				pending = append(pending, raw...)
				quiet.Reset(time.Millisecond)
			}
		}
		timer.Stop()
		quiet.Stop()
		count := min(len(pending), Chunk)
		for {
			a.mu.Lock()
			ready := a.outstanding+count <= Window
			if ready {
				a.output++
				a.sizes[a.output] = count
				a.outstanding += count
			}
			seq := a.output
			a.mu.Unlock()
			if ready {
				m.emit(a.owner, remote.Message{Type: "term_output", ID: a.epoch, Pane: a.id, Revision: seq, Data: base64.StdEncoding.EncodeToString(pending[:count])})
				break
			}
			select {
			case <-a.ctx.Done():
				return
			case <-a.credit:
			case <-time.After(10 * time.Second):
				m.emit(a.owner, remote.Message{Type: "term_exit", ID: a.epoch, Pane: a.id, Reason: "Terminal renderer stopped acknowledging output; reconnect to resume"})
				return
			}
		}
		pending = pending[count:]
	}
}

func (m *Manager) String() string {
	return fmt.Sprintf("phone terminal streams (%s)", m.Registry.Socket)
}
