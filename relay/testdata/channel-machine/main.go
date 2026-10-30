// A real Go Noise responder used by the browser integration test. It runs the
// production lease, snapshot compression and transport batch implementation.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
	"partyline.sh/partyline/internal/remote"
)

func main() {
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); os.Exit(0) }()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	origin := os.Args[1]
	machineID := strings.Repeat("11", 16)
	token := strings.Repeat("22", 32)
	phoneBytes := make([]byte, 16)
	if _, err := rand.Read(phoneBytes); err != nil {
		return err
	}
	phoneID := hex.EncodeToString(phoneBytes)
	private := make([]byte, 32)
	for i := range private {
		private[i] = 7
	}
	// Generate public keys through the same DH implementation as production.
	machine, err := noise.DH25519.GenerateKeypair(strings.NewReader(string(private)))
	if err != nil {
		return err
	}
	phonePrivate := make([]byte, 32)
	for i := range phonePrivate {
		phonePrivate[i] = 8
	}
	phone, err := noise.DH25519.GenerateKeypair(strings.NewReader(string(phonePrivate)))
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"id": machineID, "token": token, "phone": phoneID})
	response, err := http.Post(origin+"/device-token", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var issued struct {
		Token string `json:"token"`
	}
	if err = json.NewDecoder(response.Body).Decode(&issued); err != nil {
		return err
	}
	if issued.Token == "" {
		return fmt.Errorf("token issuance failed")
	}
	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, strings.Replace(origin, "http", "ws", 1)+"/connect", nil)
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	route, _ := hex.DecodeString(machineID)
	phoneRoute, _ := hex.DecodeString(phoneID)
	frame := func(kind byte, route, body []byte) []byte { return append(append([]byte{kind}, route...), body...) }
	hello, _ := json.Marshal(map[string]any{"role": "machine", "token": token, "min": 1, "max": 1})
	if err = ws.Write(ctx, websocket.MessageBinary, frame(1, route, hello)); err != nil {
		return err
	}
	_, _, err = ws.Read(ctx)
	if err != nil {
		return err
	}
	ready, _ := json.Marshal(map[string]any{"machine": machineID, "phone": phoneID, "private": phonePrivate, "public": machine.Public, "token": issued.Token})
	fmt.Println(string(ready))
	type input struct {
		body []byte
		err  error
	}
	reads := make(chan input)
	go func() {
		for {
			_, b, e := ws.Read(ctx)
			reads <- input{b, e}
			if e != nil {
				return
			}
		}
	}()
	seen := map[string]remote.Message{}
	var peer *remote.Peer
	var pending []byte
	generation := 0
	ticks := 0
	paneOutputs := 0
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case in := <-reads:
			if in.err != nil {
				return in.err
			}
			if len(in.body) >= 17 && in.body[0] == 4 {
				peer = nil
				pending = nil
				continue
			}
			if len(in.body) < 17 || in.body[0] != 2 {
				continue
			}
			if peer == nil {
				h, e := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Static: machine, PeerStatic: phone.Public})
				if e != nil {
					return e
				}
				if _, _, _, e = h.ReadMessage(nil, in.body[17:]); e != nil {
					return e
				}
				reply, receive, send, e := h.WriteMessage(nil, nil)
				if e != nil {
					return e
				}
				generation++
				pending = reply
				peer = remote.NewPeer(send, receive)
				peer.Command = func(m remote.Message, _ time.Time) remote.Message {
					if old, ok := seen[m.ID]; ok {
						return old
					}
					result := remote.Message{Type: "input_result", ID: m.ID, Status: "ok"}
					fmt.Println(string(mustJSON(m)))
					if m.Text == "refused draft" {
						result.Status, result.Reason = "refused", "test refusal"
					}
					if m.Data == "unanswered" {
						result = remote.Message{Type: "test_no_ack"}
					}
					seen[m.ID] = result
					return result
				}
				peer.Queue(remote.Message{Type: "hello", Data: map[string]any{"version": 1, "capabilities": []string{"input", "chat_send"}}})
			} else if err = peer.Read(in.body[17:], time.Now()); err != nil {
				return err
			}
		case now := <-ticker.C:
			if peer == nil {
				continue
			}
			ticks++
			wire := pending
			pending = nil
			if wire == nil {
				if err = peer.Collect(now, 0, func(topic, mode string) (any, error) {
					if os.Getenv("PWA_UI") == "1" {
						switch topic {
						case "transcript:%1":
							if os.Getenv("PWA_TRANSCRIPT") != "1" {
								return nil, nil
							}
							return []map[string]any{{"role": "user", "text": "Please run checks", "tools": []any{}}, {"role": "assistant", "text": fmt.Sprintf("%stick %d", strings.Repeat("Reply line one\nline two\n", 25), ticks), "tools": []map[string]string{{"name": "Bash", "target": "go test ./...", "status": "ok"}, {"name": "Read", "target": "missing.go", "status": "error"}, {"name": "Edit", "target": "main.go", "status": "running"}}}}, nil
						case "transcript:%2":
							return nil, nil
						case "sessions":
							return []map[string]string{{"pane": "%1", "window": "@1", "title": "Builder", "state": "working", "glyph": "●", "width": "100", "height": "30"}, {"pane": "%2", "window": "@2", "session": "contractors", "title": "Reviewer", "width": "90", "height": "24"}}, nil
						case "board":
							return []map[string]string{{"title": "Ship mobile", "section": "Running", "state": "building", "repo": "partyline"}}, nil
						case "contractors":
							return []map[string]string{{"title": "Review mobile", "role": "reviewer", "state": "running", "window": "@2"}}, nil
						case "%2":
							return "contractor review snapshot\n", nil
						case "chat":
							return map[string]any{"messages": []map[string]string{{"name": "you", "text": "Phone check", "at": "today"}}}, nil
						default:
							paneOutputs++
							return fmt.Sprintf("fresh generation %d tick %d\n\x1b]8;;javascript:window.paneScriptRan=true\x1b\\script-link\x1b]8;;\x1b\\ \x1b]8;;https://example.com/\x1b\\https-link\x1b]8;;\x1b\\", generation, ticks), nil
						}
					}
					if topic == "chat" {
						return strings.Repeat("long chat snapshot;", 6000), nil
					}
					return "\x1b[32mexpected encrypted screen\x1b[0m\n", nil
				}); err != nil {
					return err
				}
				if os.Getenv("PWA_BYE") == "1" && paneOutputs >= 2 {
					peer.Queue(remote.Message{Type: "bye", Reason: "Unreadable phone message"})
				}
				wire, err = peer.Flush()
				if err != nil {
					return err
				}
			}
			if len(wire) > 0 {
				var id [16]byte
				copy(id[:], phoneRoute)
				if err = ws.Write(ctx, websocket.MessageBinary, frame(2, route, remote.Batch(map[[16]byte][]byte{id: wire}))); err != nil {
					return err
				}
			}
		}
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
