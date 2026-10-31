package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	termansi "github.com/charmbracelet/x/ansi"
	"partyline.sh/partyline/internal/remote"
	"partyline.sh/partyline/internal/theme"
)

type remoteChannel struct {
	peers   map[string]*remote.Peer
	pending map[string][]byte
	ended   map[string][]byte
	count   int
	last    time.Time
	cursor  int
	inputs  *remoteInputStore
}

func newRemoteChannel() *remoteChannel {
	return &remoteChannel{peers: map[string]*remote.Peer{}, pending: map[string][]byte{}, ended: map[string][]byte{}, inputs: processRemoteInputs}
}
func (c *remoteChannel) drop(route string) {
	processUploads.cancelAll(route) // an upload does not outlive its session
	processResizes.releaseAll(route)
	delete(c.peers, route)
	delete(c.pending, route)
	delete(c.ended, route)
}
func (c *remoteChannel) receive(route string, b []byte, now time.Time) error {
	phones, err := remotePhones()
	if err != nil {
		return err
	}
	var phone *remotePhone
	for i := range phones {
		if phones[i].ID == route {
			phone = &phones[i]
			break
		}
	}
	if phone == nil {
		c.drop(route)
		return errors.New("unknown phone")
	}
	if p := c.peers[route]; p != nil {
		err = p.Read(b, now)
		if err != nil {
			// Use the existing send cipher so the terminal notice preserves nonce order.
			plain, _ := json.Marshal([]remote.Message{{Type: "bye", Reason: err.Error()}})
			wire, sendErr := p.Send.Encrypt(nil, nil, plain)
			c.drop(route)
			if sendErr == nil {
				c.ended[route] = wire
			}
		}
		return err
	}
	key, err := pairStatic()
	if err != nil {
		return err
	}
	h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Static: key, PeerStatic: phone.Static})
	if err != nil {
		return err
	}
	if _, _, _, err = h.ReadMessage(nil, b); err != nil {
		return err
	}
	reply, receive, send, err := h.WriteMessage(nil, nil)
	if err != nil {
		return err
	}
	delete(c.ended, route)
	c.pending[route] = reply
	p := remote.NewPeer(send, receive)
	p.Command = func(m remote.Message, now time.Time) remote.Message { return c.inputs.handle(route, m, now) }
	c.peers[route] = p
	host, _ := os.Hostname()
	p.Queue(remote.Message{Type: "hello", Data: map[string]any{"version": 1, "hostname": host, "ptlnVersion": version, "relay": remoteRelayMetadata(), "capabilities": []string{"sessions", "sub", "out", "board", "contractors", "chat", "gates", "input", "chat_send", "transcript", "uploads", "terminal_resize"}}})
	p.Queue(remote.Message{Type: "budget", Data: map[string]any{"count": c.count, "intervalMs": remote.Interval(c.count).Milliseconds(), "summaries": c.count < 80000}})
	for _, topic := range []string{"sessions", "gates"} {
		data, e := remoteSnapshot(topic, "focus")
		if e == nil {
			p.Queue(remote.Message{Type: topic, Data: data})
		}
	}
	return nil
}
func (c *remoteChannel) setBudget(count int) {
	if count < 0 {
		return
	}
	c.count = count
	for _, p := range c.peers {
		p.Queue(remote.Message{Type: "budget", Data: map[string]any{"count": count, "intervalMs": remote.Interval(count).Milliseconds(), "summaries": count < 80000}})
	}
}
func (c *remoteChannel) batch(now time.Time) ([]byte, error) {
	processResizes.tick(now)
	if !c.last.IsZero() && now.Sub(c.last) < remote.Interval(c.count) {
		return nil, nil
	}
	// Re-read the projection each window: revoke runs in a different process.
	phones, err := remotePhones()
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, p := range phones {
		allowed[p.ID] = true
	}
	entries := map[[16]byte][]byte{}
	size := 17
	routes := make([]string, 0, len(c.peers))
	for route := range c.peers {
		routes = append(routes, route)
	}
	for route := range c.ended {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	if len(routes) > 0 {
		n := c.cursor % len(routes)
		routes = append(routes[n:], routes[:n]...)
		c.cursor++
	}
	for _, route := range routes {
		p := c.peers[route]
		if !allowed[route] {
			c.drop(route)
			continue
		}
		b := c.ended[route]
		if b == nil {
			b = c.pending[route]
		}
		if b == nil {
			if err = p.Collect(now, c.count, remoteSnapshot); err != nil {
				return nil, err
			}
			// Bound each peer before encryption so a postponed peer keeps its nonce.
			// Flush is called only when this complete transport message can fit.
			if err = p.Prepare(); err != nil {
				c.drop(route)
				continue
			}
			if size+20+p.Size() > 65552 {
				continue
			}
			b, err = p.Flush()
			if err != nil {
				c.drop(route)
				continue
			}
		}
		if len(b) == 0 {
			continue
		}
		if size+20+len(b) > 65552 {
			continue
		}
		raw, e := hex.DecodeString(route)
		if e != nil || len(raw) != 16 {
			continue
		}
		var id [16]byte
		copy(id[:], raw)
		entries[id] = b
		size += 20 + len(b)
		delete(c.pending, route)
		delete(c.ended, route)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	c.last = now
	return remote.Batch(entries), nil
}
func remoteSnapshot(topic, mode string) (any, error) {
	if strings.HasPrefix(topic, "transcript:") {
		return remoteTranscript(topic)
	}
	switch topic {
	case "board":
		rows := []map[string]any{}
		for _, r := range tabRows(time.Now()) {
			rows = append(rows, remoteBoardRow(r))
		}
		return rows, nil
	case "contractors":
		// Never export contractor Env/Argv: these may contain credentials.
		rows := []map[string]any{}
		for _, c := range loadContractors() {
			rows = append(rows, map[string]any{"id": c.ID, "parent": c.Parent, "role": c.Role, "engine": c.Engine, "title": c.Title, "state": c.State, "result": c.Result, "window": c.Window})
		}
		return rows, nil
	case "chat":
		return map[string]any{"room": loadChatRoom(), "messages": remoteChatTail(readChat())}, nil
	case "gates":
		rows := []map[string]any{}
		for _, r := range loopRuns() {
			if loopWaiting(r) {
				rows = append(rows, map[string]any{"id": r.ID, "kind": r.State, "revision": r.Revision, "title": tabTitle(r)})
			}
		}
		return rows, nil
	case "sessions":
		rows := []map[string]string{}
		activities := loadWatcherState().Activity
		// Contractors live outside ptln; both sessions form the authorized pane inventory.
		// Exact targets prevent tmux's prefix matching from exposing unrelated sessions.
		for _, session := range []string{tmuxSessionName, contractorSession} {
			out, err := tmuxCmd("list-panes", "-s", "-t", "="+session+":", "-F", "#{pane_id}\t#{window_id}\t#{@ptln_key}\t#{window_name}\t#{@ptln_spec}\t#{pane_width}\t#{pane_height}").Output()
			if err != nil {
				continue
			}
			for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
				f := strings.Split(line, "\t")
				if len(f) == 7 {
					sp, _ := decodePaneSpec(f[4])
					a, ok := activities[f[2]]
					if !ok {
						a = remotePaneActivity(f[0], time.Now())
					}
					title := f[3]
					if sp.Label != "" {
						title = sp.Label
					}
					rows = append(rows, map[string]string{"pane": f[0], "width": f[5], "height": f[6], "window": f[1], "key": f[2], "title": title, "engine": sp.Engine, "state": a.State, "glyph": remoteStateGlyph(a.State), "lastLine": a.Doing, "session": session})
				}
			}
		}
		return rows, nil
	}
	// Validate against the session inventory before allowing a tmux target.
	inventory, _ := remoteSnapshot("sessions", mode)
	found := false
	if rows, ok := inventory.([]map[string]string); ok {
		for _, r := range rows {
			if r["pane"] == topic {
				found = true
			}
		}
	}
	if !found {
		return nil, errors.New("pane unavailable")
	}
	out, err := tmuxCmd("capture-pane", "-e", "-p", "-t", topic).Output()
	if err != nil {
		return nil, err
	}
	if mode == "summary" {
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		a := screenActivity(screenBottom(termansi.Strip(string(out)), 15))
		return map[string]string{"lastLine": termansi.Strip(lines[len(lines)-1]), "state": a.State, "glyph": remoteStateGlyph(a.State)}, nil
	}
	return string(out), nil
}

func remoteStateGlyph(state string) string {
	switch state {
	case "working":
		return theme.Working.Glyph()
	case "approval":
		return theme.NeedsYou.Glyph()
	default:
		return theme.Idle.Glyph()
	}
}

// remoteBoardRow is a Loops card for the phone. The column goes by its name ("In review"), as the
// board shows it: its number is the board's internal order, which a phone cannot interpret.
func remoteBoardRow(r tabRow) map[string]any {
	return map[string]any{"id": r.RunID, "section": tabSectionNames[r.Section], "repo": r.Repo, "title": r.Title, "state": r.State, "batch": r.Batch, "batchId": r.BatchID, "detail": r.Detail, "watch": r.Watch, "round": r.Round, "openP1": r.OpenP1, "dispute": r.Dispute, "reset": r.Reset, "chips": r.Chips, "pr": r.PR, "ci": r.CI, "builder": r.Builder}
}

// remoteChatTail is the end of the room a phone gets: at most 100 messages and 40 KB encoded. The
// whole history (800 KB on a busy machine) went out as dozens of 12 KB fragments, half a second
// apart, and every new message queued it again: a phone's chat, input results and pane snapshots
// waited behind it for ever.
func remoteChatTail(msgs []chatMessage) []chatMessage {
	const maxMessages, maxBytes = 100, 40 << 10
	start, size := len(msgs), 0
	for start > 0 && len(msgs)-start < maxMessages {
		b, _ := json.Marshal(msgs[start-1])
		if size+len(b) > maxBytes && start < len(msgs) {
			break
		}
		size += len(b)
		start--
	}
	return msgs[start:]
}
