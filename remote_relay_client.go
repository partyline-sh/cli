package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func relayHello(c relayConfig, token string) []byte {
	body, _ := json.Marshal(map[string]any{"role": "machine", "token": token, "min": 1, "max": 1})
	id, _ := hex.DecodeString(c.MachineID)
	return append(append([]byte{1}, id...), body...)
}
func relayBackoff(attempt int) time.Duration {
	if attempt > 9 {
		attempt = 9
	}
	ceiling := time.Second * time.Duration(1<<attempt)
	if ceiling > 5*time.Minute {
		ceiling = 5 * time.Minute
	}
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
}

var relayDialHTTP = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func relaySession(ctx context.Context, c relayConfig, token string) error {
	u := strings.Replace(c.URL, "https://", "wss://", 1) + "/connect"
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, u, &websocket.DialOptions{HTTPClient: relayDialHTTP})
	dialCancel()
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(65552)
	handshake, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = conn.Write(handshake, websocket.MessageBinary, relayHello(c, token))
	if err != nil {
		cancel()
		return err
	}
	kind, body, err := conn.Read(handshake)
	cancel()
	if err != nil {
		return err
	}
	var ack struct {
		OK      bool `json:"ok"`
		Version int  `json:"version"`
	}
	if kind != websocket.MessageBinary || len(body) < 17 || body[0] != 1 || string(body[1:17]) != string(relayHello(c, token)[1:17]) || json.Unmarshal(body[17:], &ack) != nil || !ack.OK || ack.Version != 1 {
		return websocket.CloseError{Code: websocket.StatusPolicyViolation, Reason: "invalid hello acknowledgment"}
	}
	// Bound retry work and keep queue/HTTP failures from terminating the session.
	revokeCtx, stopRevokes := context.WithTimeout(ctx, 5*time.Second)
	_ = flushRemoteRevokes(revokeCtx, c, token)
	stopRevokes()
	// Application ping/pong is answered by the DO without waking it.
	pingCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingCtx.Done():
				return
			case <-ticker.C:
				writeCtx, cancel := context.WithTimeout(pingCtx, 10*time.Second)
				err := conn.Write(writeCtx, websocket.MessageText, []byte("ping"))
				cancel()
				if err != nil {
					conn.CloseNow()
					return
				}
			}
		}
	}()
	write := func(body []byte) error {
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return conn.Write(writeCtx, websocket.MessageBinary, body)
	}
	idle := time.NewTimer(75 * time.Second)
	defer idle.Stop()
	channel := newRemoteChannel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	type incoming struct {
		kind websocket.MessageType
		body []byte
		err  error
	}
	reads := make(chan incoming)
	go func() {
		for {
			kind, body, err := conn.Read(pingCtx)
			select {
			case reads <- incoming{kind, body, err}:
			case <-pingCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var pairing *pairSession
	var pairingRoute string
	type pairReply struct {
		route []byte
		body  []byte
		err   error
	}
	pairReplies := make(chan pairReply, 1)
	approving := false
	for {
		var kind websocket.MessageType
		var body []byte
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle.C:
			return errors.New("relay idle timeout")
		case reply := <-pairReplies:
			approving = false
			if reply.err == nil {
				frame := append(append([]byte{2}, reply.route...), reply.body...)
				if err := write(frame); err != nil {
					return err
				}
			}
			continue
		case <-ticker.C:
			batch, e := channel.batch(time.Now())
			if e != nil {
				return e
			}
			if len(batch) > 0 {
				frame := append(append([]byte{2}, relayHello(c, token)[1:17]...), batch...)
				if e = write(frame); e != nil {
					return e
				}
				channel.last = time.Now()
			}
			continue
		case in := <-reads:
			idle.Reset(75 * time.Second)
			if in.err != nil {
				return in.err
			}
			kind, body = in.kind, in.body
		}
		if kind == websocket.MessageBinary && len(body) >= 17 {
			route := hex.EncodeToString(body[1:17])
			if body[0] == 4 {
				channel.drop(route)
				continue
			}
			if body[0] == 6 {
				var budget struct {
					Count int `json:"count"`
				}
				if json.Unmarshal(body[17:], &budget) == nil {
					channel.setBudget(budget.Count)
				}
				continue
			}
			if body[0] == 2 {
				phones, e := remotePhones()
				if e != nil {
					return e
				}
				known := false
				for _, p := range phones {
					if p.ID == route {
						known = true
						break
					}
				}
				if known {
					_ = channel.receive(route, body[17:], time.Now())
					continue
				}
			}
		}
		if kind == websocket.MessageBinary && len(body) >= 17 && body[0] == 2 {
			route := hex.EncodeToString(body[1:17])
			var reply []byte
			var e error
			if pairing != nil && route == pairingRoute {
				session := pairing
				pairing = nil
				p, err := session.enroll(c, body[17:])
				if err != nil {
					continue
				}
				approving = true
				replyRoute := append([]byte(nil), body[1:17]...)
				go func() {
					approvalCtx, cancel := context.WithDeadline(pingCtx, session.expires)
					defer cancel()
					device, err := requestPairApproval(approvalCtx, p, session.enrollment, session.challenge)
					result, _ := json.Marshal(map[string]any{"approved": err == nil, "token": device, "machine": c.MachineID, "fingerprint": p.Fingerprint, "phone": p.ID})
					encrypted, e := session.send.Encrypt(nil, nil, result)
					select {
					case pairReplies <- pairReply{replyRoute, encrypted, e}:
					case <-pingCtx.Done():
					}
				}()
				continue
			} else {
				if approving {
					continue
				}
				if pairing != nil && time.Now().Before(pairing.expires) {
					continue
				}
				reply, pairing, e = beginPair(route, body[17:])
				pairingRoute = route
			}
			if e == nil {
				frame := append(append([]byte{2}, body[1:17]...), reply...)
				if e = write(frame); e != nil {
					return e
				}
			}
		}
	}
}

// Started once by the watcher, including when relay.json appears after watcher startup.
func watchRelay(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		c, t, err := loadRelay()
		if err == nil {
			started := time.Now()
			_ = relaySession(ctx, c, t)
			if time.Since(started) > time.Minute {
				attempt = 0
			}
		}
		delay := relayBackoff(attempt)
		if err != nil {
			delay = time.Minute
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err == nil {
			attempt++
		}
	}
}
