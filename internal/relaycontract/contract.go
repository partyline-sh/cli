// Package relaycontract runs transport acceptance checks without any runtime APIs.
// The same checks are used against the Go server and the local Cloudflare Worker.
package relaycontract

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type Fixture struct {
	Origin, Machine, Token string
	HTTP                   *http.Client
}
type client struct {
	t *testing.T
	f Fixture
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func envelope(kind byte, id string, b []byte) []byte {
	route, _ := hex.DecodeString(id)
	return append(append([]byte{kind}, route...), b...)
}
func (c client) post(path string, body any, status int) map[string]json.RawMessage {
	c.t.Helper()
	b, _ := json.Marshal(body)
	r, err := c.f.HTTP.Post(c.f.Origin+path, "application/json", bytes.NewReader(b))
	if err != nil {
		c.t.Fatal(err)
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		c.t.Fatal(err)
	}
	if r.StatusCode != status {
		c.t.Fatalf("%s: HTTP %d, want %d (%s)", path, r.StatusCode, status, data)
	}
	var out map[string]json.RawMessage
	if status == 200 && json.Unmarshal(data, &out) != nil {
		c.t.Fatalf("invalid JSON response from %s", path)
	}
	return out
}
func stringField(b map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(b[key], &s)
	return s
}
func (c client) owner() map[string]string {
	return map[string]string{"id": c.f.Machine, "token": c.f.Token}
}
func (c client) token(machine, token, phone string) string {
	return stringField(c.post("/device-token", map[string]string{"id": machine, "token": token, "phone": phone}, 200), "token")
}
func (c client) socket() *websocket.Conn {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(c.f.Origin, "http", "ws", 1)+"/connect", &websocket.DialOptions{HTTPClient: c.f.HTTP})
	if err != nil {
		c.t.Fatal(err)
	}
	ws.SetReadLimit(65552)
	c.t.Cleanup(func() { ws.CloseNow() })
	return ws
}
func (c client) send(ws *websocket.Conn, b []byte) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageBinary, b); err != nil {
		c.t.Fatal(err)
	}
}
func (c client) read(ws *websocket.Conn) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		kind, b, err := ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if kind == websocket.MessageText {
			continue
		}
		if len(b) >= 17 && b[0] == 6 {
			continue
		}
		return b, nil
	}
}
func (c client) hello(ws *websocket.Conn, id, role, token, machine string) {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"role": role, "token": token, "machine": machine, "min": 1, "max": 1})
	c.send(ws, envelope(1, id, b))
	ack, err := c.read(ws)
	if err != nil {
		c.t.Fatal(err)
	}
	var a struct {
		OK      bool
		Version int
	}
	if len(ack) < 17 || ack[0] != 1 || hex.EncodeToString(ack[1:17]) != id || json.Unmarshal(ack[17:], &a) != nil || !a.OK || a.Version != 1 {
		c.t.Fatal("invalid hello ack")
	}
}
func (c client) expect(ws *websocket.Conn, want []byte) {
	c.t.Helper()
	b, err := c.read(ws)
	if err != nil || !bytes.Equal(b, want) {
		c.t.Fatalf("relay payload mismatch: len=%d want=%d error=%v", len(b), len(want), err)
	}
}
func (c client) batch(machine, route string, payload []byte) []byte {
	id, _ := hex.DecodeString(route)
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, uint32(len(payload)))
	return envelope(2, machine, append(append(id, size...), payload...))
}

func Run(t *testing.T, f Fixture) {
	t.Helper()
	if f.HTTP == nil {
		f.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	c := client{t, f}
	t.Run("registry and single use computer invites", func(t *testing.T) {
		c := client{t, f}
		c.post("/status", map[string]string{"id": f.Machine, "token": strings.Repeat("0", 64)}, 403)
		inv := stringField(c.post("/invite", c.owner(), 200), "invite")
		second := randomID()
		r := c.post("/join", map[string]string{"id": second, "invite": inv}, 200)
		token := stringField(r, "token")
		if len(token) != 64 {
			t.Fatal("invalid token")
		}
		c.post("/join", map[string]string{"id": randomID(), "invite": inv}, 403)
		c.post("/status", map[string]string{"id": second, "token": token}, 200)
		// An identity collision must roll back consuming the invite.
		inv = stringField(c.post("/invite", c.owner(), 200), "invite")
		c.post("/join", map[string]string{"id": second, "invite": inv}, 403)
		c.post("/join", map[string]string{"id": randomID(), "invite": inv}, 200)
	})
	t.Run("opaque bidirectional routes and revocation", func(t *testing.T) {
		c := client{t, f}
		phone := randomID()
		token := c.token(f.Machine, f.Token, phone)
		c.post("/device-token", map[string]string{"id": f.Machine, "token": f.Token, "phone": phone}, 403)
		machine := c.socket()
		c.hello(machine, f.Machine, "machine", f.Token, "")
		p := c.socket()
		c.hello(p, phone, "phone", token, f.Machine)
		payload := bytes.Repeat([]byte{0x88}, 32000)
		b := envelope(2, phone, payload)
		c.send(p, b)
		c.expect(machine, b)
		c.send(machine, c.batch(f.Machine, phone, payload))
		c.expect(p, b)
		c.post("/device-revoke", map[string]string{"id": f.Machine, "token": f.Token, "phone": phone}, 200)
		if _, err := c.read(p); err == nil {
			t.Fatal("revoked phone remained connected")
		}
		gone, err := c.read(machine)
		if err != nil || len(gone) < 17 || gone[0] != 4 || hex.EncodeToString(gone[1:17]) != phone {
			t.Fatal("machine did not receive revoked route notification")
		}
	})
	t.Run("phone cannot select another route", func(t *testing.T) {
		c := client{t, f}
		phone := randomID()
		token := c.token(f.Machine, f.Token, phone)
		p := c.socket()
		c.hello(p, phone, "phone", token, f.Machine)
		c.send(p, envelope(2, randomID(), bytes.Repeat([]byte{1}, 16)))
		if _, err := c.read(p); err == nil {
			t.Fatal("cross-route write accepted")
		}
	})
	t.Run("two computers keep identical phone ids isolated", func(t *testing.T) {
		c := client{t, f}
		invite := stringField(c.post("/invite", c.owner(), 200), "invite")
		second := randomID()
		owner := stringField(c.post("/join", map[string]string{"id": second, "invite": invite}, 200), "token")
		phone := randomID()
		tokenA := c.token(f.Machine, f.Token, phone)
		tokenB := c.token(second, owner, phone)
		mA := c.socket()
		c.hello(mA, f.Machine, "machine", f.Token, "")
		mB := c.socket()
		c.hello(mB, second, "machine", owner, "")
		pA := c.socket()
		c.hello(pA, phone, "phone", tokenA, f.Machine)
		pB := c.socket()
		c.hello(pB, phone, "phone", tokenB, second)
		a := bytes.Repeat([]byte{0xa1}, 32)
		b := bytes.Repeat([]byte{0xb2}, 32)
		c.send(pA, envelope(2, phone, a))
		c.expect(mA, envelope(2, phone, a))
		c.send(pB, envelope(2, phone, b))
		c.expect(mB, envelope(2, phone, b))
		c.send(mA, c.batch(f.Machine, phone, a))
		c.expect(pA, envelope(2, phone, a))
		c.send(mB, c.batch(second, phone, b))
		c.expect(pB, envelope(2, phone, b))
		p := c.socket()
		hello, _ := json.Marshal(map[string]any{"role": "phone", "machine": second, "token": tokenA, "min": 1, "max": 1})
		c.send(p, envelope(1, phone, hello))
		if _, err := c.read(p); err == nil {
			t.Fatal("one computer's phone credential authorized another")
		}
	})
	t.Run("invalid batch forwards no partial content", func(t *testing.T) {
		c := client{t, f}
		phone := randomID()
		token := c.token(f.Machine, f.Token, phone)
		m := c.socket()
		c.hello(m, f.Machine, "machine", f.Token, "")
		p := c.socket()
		c.hello(p, phone, "phone", token, f.Machine)
		batch := append(c.batch(f.Machine, phone, bytes.Repeat([]byte{7}, 32)), 0xff)
		c.send(m, batch)
		// Complete the WebSocket close handshake, as the production machine's read
		// loop does. The Worker emits gone from its socket-close callback.
		if _, err := c.read(m); err == nil {
			t.Fatal("malformed batch did not close machine")
		}
		got, err := c.read(p)
		if err != nil || len(got) < 17 || got[0] != 4 {
			t.Fatal("invalid batch delivered content or omitted disconnect notification")
		}
	})
	t.Run("reconnect preserves credentials", func(t *testing.T) {
		c := client{t, f}
		phone := randomID()
		token := c.token(f.Machine, f.Token, phone)
		p := c.socket()
		c.hello(p, phone, "phone", token, f.Machine)
		p.CloseNow()
		p = c.socket()
		c.hello(p, phone, "phone", token, f.Machine)
	})
	t.Run("machine disconnect notifies its phone", func(t *testing.T) {
		c := client{t, f}
		phone := randomID()
		token := c.token(f.Machine, f.Token, phone)
		m := c.socket()
		c.hello(m, f.Machine, "machine", f.Token, "")
		p := c.socket()
		c.hello(p, phone, "phone", token, f.Machine)
		m.CloseNow()
		b, err := c.read(p)
		if err != nil || len(b) < 17 || b[0] != 4 {
			t.Fatal("missing gone frame")
		}
	})
	t.Run("malformed hello refused", func(t *testing.T) {
		c := client{t, f}
		p := c.socket()
		c.send(p, envelope(1, randomID(), []byte(`{"role":"machine","token":"bad","min":1,"max":1}`)))
		if _, err := c.read(p); err == nil {
			t.Fatal("unauthorized hello accepted")
		}
	})
	t.Run("keepalive text ping", func(t *testing.T) {
		c := client{t, f}
		p := c.socket()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := p.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
			t.Fatal(err)
		}
		kind, b, err := p.Read(ctx)
		if err != nil || kind != websocket.MessageText || string(b) != "pong" {
			t.Fatal("keepalive failed")
		}
	})
	t.Run("authenticated idle connection survives HTTP timeouts", func(t *testing.T) {
		c := client{t, f}
		m := c.socket()
		c.hello(m, f.Machine, "machine", f.Token, "")
		for i := 0; i < 4; i++ {
			time.Sleep(6 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := m.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
				cancel()
				t.Fatal(err)
			}
			for {
				kind, b, err := m.Read(ctx)
				if err != nil {
					cancel()
					t.Fatal("idle authenticated socket expired", err)
				}
				if kind == websocket.MessageText && string(b) == "pong" {
					break
				}
				if kind != websocket.MessageBinary || len(b) < 17 || b[0] != 6 {
					cancel()
					t.Fatal("unexpected keepalive response")
				}
			}
			cancel()
		}
		status := c.post("/status", c.owner(), 200)
		var connected bool
		_ = json.Unmarshal(status["connected"], &connected)
		if !connected {
			t.Fatal("idle machine was disconnected")
		}
	})
	_ = c
}
