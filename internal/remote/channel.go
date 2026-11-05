package remote

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/flynn/noise"
	"github.com/klauspost/compress/zstd"
)

// Batch entries are route(16), ciphertext length(4, big endian), ciphertext.
// The relay validates only this envelope, never the ciphertext.
func Batch(entries map[[16]byte][]byte) []byte {
	var out []byte
	for route, b := range entries {
		out = append(out, route[:]...)
		out = binary.BigEndian.AppendUint32(out, uint32(len(b)))
		out = append(out, b...)
	}
	return out
}

const MaxInputBytes = 24 * 1024
const MaxCiphertextBytes = 65535

type Message struct {
	ID       string `json:"id,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Text     string `json:"text,omitempty"`
	Status   string `json:"status,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Type     string `json:"type"`
	Pane     string `json:"pane,omitempty"`
	Target   string `json:"target,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
	Data     any    `json:"data,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}
type lease struct {
	mode        string
	until, sent time.Time
	previous    string
	revision    uint64
}
type Peer struct {
	DetectAgents bool // client binds adopted-agent input to its foreground process
	// Command is installed by the authenticated route owner; results stay on this peer.
	Command       func(Message, time.Time) Message
	Send, Receive *noise.CipherState
	leases        map[string]*lease
	control       []Message // command results (input_result), sent ahead of queued snapshots
	pending       []Message
	wire          [][]byte
	transfer      uint64
	wireUntil     time.Time
	fragmented    bool
	wireSummary   bool
}

func NewPeer(send, receive *noise.CipherState) *Peer {
	return &Peer{Send: send, Receive: receive, leases: map[string]*lease{}}
}
func (p *Peer) Read(ciphertext []byte, now time.Time) error {
	if len(ciphertext) > 65535 {
		return errors.New("oversize ciphertext")
	}
	plain, err := p.Receive.Decrypt(nil, nil, ciphertext)
	if err != nil {
		return err
	}
	var messages []Message
	if err = json.Unmarshal(plain, &messages); err != nil {
		return err
	}
	if len(messages) > 128 {
		return errors.New("too many subscriptions")
	}
	for _, m := range messages {
		switch m.Type {
		case "terminal_close", "terminal_history", "session_list", "session_open", "session_close", "term_open", "term_list", "term_input", "term_ack", "term_resize", "term_detach", "term_close", "live_open", "live_input", "live_close", "live_ping", "shell_open", "loop_detail", "loop_prepare", "loop_action", "input", "input_submit", "chat_send", "chat_limit", "upload_begin", "upload_chunk", "upload_finish", "upload_cancel", "resize":
			if p.Command == nil {
				return errors.New("input unavailable")
			}
			// A result goes ahead of snapshot fragments already queued: behind a large snapshot
			// it outlived the phone's 5-second wait and the input read as "delivery unknown".
			if result := p.Command(m, now); result.Type != "" {
				p.control = append(p.control, result)
			}
		case "resync":
			if l := p.leases[m.Pane]; l != nil {
				l.previous = ""
				l.sent = time.Time{}
			}
		case "hello":
			if m.Data != nil {
				raw, _ := json.Marshal(m.Data)
				var hello struct {
					Capabilities []string `json:"capabilities"`
				}
				if err := json.Unmarshal(raw, &hello); err != nil || len(hello.Capabilities) > 32 {
					return errors.New("invalid client capabilities")
				}
				for _, feature := range hello.Capabilities {
					if feature == "detected_agents" {
						p.DetectAgents = true
					}
				}
			}
		case "sub":
			if m.Pane == "" || len(m.Pane) > 128 || (m.Mode != "focus" && m.Mode != "summary") {
				return errors.New("invalid subscription")
			}
			if _, ok := p.leases[m.Pane]; !ok && len(p.leases) >= 64 {
				return errors.New("too many leases")
			}
			old := p.leases[m.Pane]
			if old == nil || !now.Before(old.until) {
				old = &lease{}
				p.leases[m.Pane] = old
			}
			old.mode = m.Mode
			old.until = now.Add(60 * time.Second)
		default:
			return errors.New("unsupported message")
		}
	}
	return nil
}

// QueueControl sends command results ahead of queued snapshots, in the order given: an input's
// final result and its later delivery outcome come from workers after the command returned.
func (p *Peer) QueueControl(messages ...Message) {
	p.control = append(p.control, messages...)
}

func (p *Peer) Queue(messages ...Message) {
	for _, m := range messages {
		for i := 0; i < len(p.pending); i++ {
			if p.pending[i].Type == m.Type && p.pending[i].Pane == m.Pane && p.pending[i].ID == m.ID {
				p.pending = append(p.pending[:i], p.pending[i+1:]...)
				break
			}
		}
		p.pending = append(p.pending, m)
	}
}
func Interval(count int) time.Duration {
	if count >= 80000 {
		return 5 * time.Second
	}
	if count >= 50000 {
		return time.Second
	}
	return 500 * time.Millisecond
}

// Collect only calls snapshot for a live, due lease. A reconnect creates a new
// Peer and therefore always sends complete snapshots rather than deltas.
func (p *Peer) Collect(now time.Time, count int, snapshot func(string, string) (any, error)) error {
	if len(p.wire) > 0 && ((!p.wireUntil.IsZero() && !now.Before(p.wireUntil)) || count >= 80000 && p.wireSummary) {
		p.wire = nil
		if p.fragmented {
			p.Queue(Message{Type: "reset"})
		}
		for _, l := range p.leases {
			l.previous = ""
			l.sent = time.Time{}
		}
	}
	// Drop queued snapshots whose lease expired, including when another peer
	// filled the last batch and these snapshots never reached encryption.
	kept := p.pending[:0]
	for _, m := range p.pending {
		key := m.Type
		if m.Type == "out" {
			key = m.Pane
		}
		l := p.leases[key]
		if l == nil || now.Before(l.until) && !(l.mode == "summary" && count >= 80000) {
			kept = append(kept, m)
		}
	}
	p.pending = kept
	for key, l := range p.leases {
		if !now.Before(l.until) {
			delete(p.leases, key)
			continue
		}
		interval := Interval(count)
		if l.mode == "summary" {
			if count >= 80000 {
				continue
			}
			interval = 10 * time.Second
		}
		if !l.sent.IsZero() && now.Sub(l.sent) < interval {
			continue
		}
		transcript := strings.HasPrefix(key, "transcript:")
		data, err := snapshot(key, l.mode)
		if err != nil {
			if !transcript {
				continue
			}
			data = nil
		}
		b, err := json.Marshal(data)
		if err != nil {
			if transcript {
				continue
			}
			return err
		}
		if transcript && len(b) > 48*1024 {
			continue
		}
		l.sent = now
		if string(b) == l.previous {
			continue
		}
		l.previous = string(b)
		l.revision++
		m := Message{Type: key, Data: data, Revision: l.revision}
		if transcript && data == nil {
			m.Data = json.RawMessage("null")
		}
		if !transcript && key != "board" && key != "contractors" && key != "chat" && key != "sessions" && key != "gates" {
			m.Type = "out"
			m.Pane = key
			if l.mode == "focus" {
				text, ok := data.(string)
				if !ok {
					return errors.New("pane snapshot must be text")
				}
				enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
				if err != nil {
					return err
				}
				m.Data = enc.EncodeAll([]byte(text), nil)
				enc.Close()
				m.Encoding = "zstd"
			}
		}
		p.Queue(m)
	}
	return nil
}

// Prepare fragments before encrypting, so postponed batches do not consume nonces.
// Fragments are authenticated individually and assembled only inside the phone.
func (p *Peer) Prepare() error {
	if len(p.wire) > 0 || len(p.pending) == 0 {
		return nil
	}
	p.wireUntil = time.Time{}
	p.wireSummary = false
	for _, m := range p.pending {
		key := m.Type
		if m.Type == "out" {
			key = m.Pane
		}
		if l := p.leases[key]; l != nil {
			if p.wireUntil.IsZero() || l.until.Before(p.wireUntil) {
				p.wireUntil = l.until
			}
			if l.mode == "summary" {
				p.wireSummary = true
			}
		}
	}
	b, err := json.Marshal(p.pending)
	if err != nil {
		return err
	}
	if len(b) > 8*1024*1024 {
		return errors.New("snapshot too large")
	}
	p.fragmented = len(b) > 24000
	if len(b) <= 24000 {
		p.wire = append(p.wire, b)
	} else {
		p.transfer++
		parts := (len(b) + 11999) / 12000
		for i := 0; i < parts; i++ {
			end := min((i+1)*12000, len(b))
			fragment, _ := json.Marshal([]any{map[string]any{"type": "fragment", "id": p.transfer, "index": i, "parts": parts, "data": base64.StdEncoding.EncodeToString(b[i*12000 : end])}})
			p.wire = append(p.wire, fragment)
		}
	}
	p.pending = nil
	return nil
}
func (p *Peer) Size() int {
	if b, _ := p.controlFrame(); b != nil {
		return len(b) + 16
	}
	if len(p.wire) == 0 {
		return 0
	}
	return len(p.wire[0]) + 16
}

// controlFrameLimit keeps a frame of command results, plus the AEAD tag, inside one Noise message.
const controlFrameLimit = MaxCiphertextBytes - 16

// controlFrame is the longest run of queued command results, in order, that fits one frame, and
// how many it holds; nil when none are queued. The phone delivers ordinary messages arriving
// between a snapshot's fragments; only the fragments' own order matters.
//
// A frame over the limit can never be sent, and the batcher skips a peer whose next frame is too
// large, so one big result (loop details may reach 512 KiB) used to stall that phone for good.
// A result too large for any frame on its own moves to the snapshot queue, which fragments it.
// It then arrives after the results queued behind it; the phone matches results by id.
func (p *Peer) controlFrame() ([]byte, int) {
	for len(p.control) > 0 {
		// A JSON array is its elements joined by commas inside brackets, so the frame grows one
		// marshalled result at a time rather than re-encoding the whole run for each one.
		frame := []byte{'['}
		n := 0
		for _, m := range p.control {
			b, err := json.Marshal(m)
			sep := 0
			if n > 0 {
				sep = 1 // the comma before this element
			}
			if err != nil || len(frame)+sep+len(b)+1 > controlFrameLimit { // +1 for the closing ]
				break
			}
			if sep > 0 {
				frame = append(frame, ',')
			}
			frame, n = append(frame, b...), n+1
		}
		if n > 0 {
			return append(frame, ']'), n
		}
		p.pending = append(p.pending, p.control[0])
		p.control = p.control[1:]
	}
	return nil, 0
}
func (p *Peer) Flush() ([]byte, error) {
	if b, n := p.controlFrame(); b != nil {
		out, err := p.Send.Encrypt(nil, nil, b)
		if err == nil {
			p.control = p.control[n:]
		}
		return out, err
	}
	if err := p.Prepare(); err != nil {
		return nil, err
	}
	if len(p.wire) == 0 {
		return nil, nil
	}
	out, err := p.Send.Encrypt(nil, nil, p.wire[0])
	if err == nil {
		p.wire = p.wire[1:]
	}
	return out, err
}

// EncryptImmediate is called only by the connection's writer loop. It preserves
// the Noise nonce order and never touches snapshot fragments or replacement queues.
func (p *Peer) EncryptImmediate(message Message) ([]byte, error) {
	raw, err := json.Marshal([]Message{message})
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxCiphertextBytes-16 {
		return nil, errors.New("interactive frame too large")
	}
	return p.Send.Encrypt(nil, nil, raw)
}

// FlushTerminalControl bypasses snapshot cadence for synchronous stream refusals.
// Normal stream results use the asynchronous event lane.
func (p *Peer) FlushTerminalControl() ([]byte, error) {
	var immediate, keep []Message
	for _, m := range p.control {
		if strings.HasPrefix(m.Type, "term_") {
			immediate = append(immediate, m)
		} else {
			keep = append(keep, m)
		}
	}
	if len(immediate) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(immediate)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxCiphertextBytes-16 {
		return nil, errors.New("terminal control frame too large")
	}
	wire, err := p.Send.Encrypt(nil, nil, raw)
	if err == nil {
		p.control = keep
	}
	return wire, err
}
