// Package gatewayjoin binds a computer join to a code compared on the gateway.
// A discovered hostname is never treated as authorization. The approved machine
// token is sent inside the Noise session, not as a discovery or polling response.
package gatewayjoin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
)

type Request struct {
	Machine string `json:"machine"`
	Name    string `json:"name"`
}
type Result struct {
	Approved bool   `json:"approved"`
	Token    string `json:"token,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
type Channel struct {
	Code, ID      string
	Request       Request
	conn          *websocket.Conn
	send, receive *noise.CipherState
}

func handshake(key noise.DHKey, initiator bool) (*noise.HandshakeState, error) {
	return noise.NewHandshakeState(noise.Config{CipherSuite: noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s), Pattern: noise.HandshakeXX, Initiator: initiator, StaticKeypair: key, Prologue: []byte("ptln-private-gateway-join-v2")})
}
func read(ctx context.Context, c *websocket.Conn) ([]byte, error) {
	kind, b, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageBinary || len(b) > 4096 {
		return nil, errors.New("invalid join frame")
	}
	return b, nil
}
func completed(c *websocket.Conn, h *noise.HandshakeState, a, b *noise.CipherState, initiator bool, request Request) *Channel {
	binding := sha256.Sum256(append([]byte("ptln join code v2"), h.ChannelBinding()...))
	ch := &Channel{Code: fmt.Sprintf("%06d", binary.BigEndian.Uint32(binding[:4])%1000000), ID: hex.EncodeToString(binding[4:20]), conn: c, Request: request, send: b, receive: a}
	if initiator {
		ch.send, ch.receive = a, b
	}
	return ch
}
func CleanName(name string) string {
	out := []rune{}
	for _, r := range name {
		if !unicode.IsControl(r) && unicode.IsPrint(r) {
			out = append(out, r)
		}
		if len(out) >= 48 {
			break
		}
	}
	if len(out) == 0 {
		return "Unnamed computer"
	}
	return string(out)
}

func Begin(ctx context.Context, c *websocket.Conn, request Request) (*Channel, error) {
	c.SetReadLimit(4096)
	key, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, err
	}
	h, err := handshake(key, true)
	if err != nil {
		return nil, err
	}
	request.Name = CleanName(request.Name)
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, sha256.Size)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	commitment := joinCommitment(key.Public, nonce, payload)
	first, _, _, err := h.WriteMessage(nil, commitment[:])
	if err != nil {
		return nil, err
	}
	if err = c.Write(ctx, websocket.MessageBinary, first); err != nil {
		return nil, err
	}
	second, err := read(ctx, c)
	if err != nil {
		return nil, err
	}
	if _, _, _, err = h.ReadMessage(nil, second); err != nil {
		return nil, err
	}
	third, a, b, err := h.WriteMessage(nil, append(nonce, payload...))
	if err != nil {
		return nil, err
	}
	if err = c.Write(ctx, websocket.MessageBinary, third); err != nil {
		return nil, err
	}
	if a == nil || b == nil {
		return nil, errors.New("incomplete join handshake")
	}
	return completed(c, h, a, b, true, request), nil
}
func Accept(ctx context.Context, c *websocket.Conn, key noise.DHKey) (*Channel, error) {
	c.SetReadLimit(4096)
	h, err := handshake(key, false)
	if err != nil {
		return nil, err
	}
	first, err := read(ctx, c)
	if err != nil {
		return nil, err
	}
	commitment, _, _, err := h.ReadMessage(nil, first)
	if err != nil {
		return nil, err
	}
	if len(commitment) != sha256.Size {
		return nil, errors.New("missing join commitment")
	}
	second, _, _, err := h.WriteMessage(nil, nil)
	if err != nil {
		return nil, err
	}
	if err = c.Write(ctx, websocket.MessageBinary, second); err != nil {
		return nil, err
	}
	third, err := read(ctx, c)
	if err != nil {
		return nil, err
	}
	payload, a, b, err := h.ReadMessage(nil, third)
	if err != nil {
		return nil, err
	}
	payload, err = revealJoin(commitment, h.PeerStatic(), payload)
	if err != nil {
		return nil, err
	}
	var request Request
	if a == nil || b == nil || json.Unmarshal(payload, &request) != nil {
		return nil, errors.New("invalid join request")
	}
	id, err := hex.DecodeString(request.Machine)
	if err != nil || len(id) != 16 {
		return nil, errors.New("invalid joining machine identity")
	}
	request.Name = CleanName(request.Name)
	return completed(c, h, a, b, false, request), nil
}
func (c *Channel) Send(ctx context.Context, result Result) error {
	b, _ := json.Marshal(result)
	encrypted, err := c.send.Encrypt(nil, nil, b)
	if err != nil {
		return err
	}
	return c.conn.Write(ctx, websocket.MessageBinary, encrypted)
}
func (c *Channel) Receive(ctx context.Context) (Result, error) {
	var out Result
	b, err := read(ctx, c.conn)
	if err != nil {
		return out, err
	}
	b, err = c.receive.Decrypt(nil, nil, b)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

// WaitClosed consumes no application payload while approval is pending. Any
// requester data after the handshake cancels the request; no reconnect replay.
func (c *Channel) WaitClosed(ctx context.Context) error {
	_, err := read(ctx, c.conn)
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}
