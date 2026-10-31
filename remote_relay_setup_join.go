package main

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"github.com/coder/websocket"
	"os"
	"partyline.sh/partyline/internal/gatewayjoin"
	"strings"
	"time"
)

func scanRelayCmd() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	entries, err := discoverRelays(ctx)
	return relayDiscoveryMsg{Entries: entries, Err: err}
}
func startGatewayJoin(origin string) tea.Msg {
	if err := requireUnconfiguredRelay(); err != nil {
		return relayJoinReadyMsg{Err: err}
	}
	id, err := relayRandom(16)
	if err != nil {
		return relayJoinReadyMsg{Err: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	dial, done := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dial, strings.Replace(origin, "https://", "wss://", 1)+"/join-connect", &websocket.DialOptions{HTTPClient: relayDialHTTP})
	done()
	if err != nil {
		cancel()
		return relayJoinReadyMsg{Err: fmt.Errorf("cannot reach this gateway over trusted HTTPS: %w", err)}
	}
	handshake, stop := context.WithTimeout(ctx, 10*time.Second)
	name, _ := os.Hostname()
	channel, err := gatewayjoin.Begin(handshake, conn, gatewayjoin.Request{Machine: id, Name: name})
	stop()
	if err != nil {
		conn.CloseNow()
		cancel()
		return relayJoinReadyMsg{Err: err}
	}
	return relayJoinReadyMsg{Channel: channel, Conn: conn, Machine: id, Origin: origin, Cancel: cancel}
}
