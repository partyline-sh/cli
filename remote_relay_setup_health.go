package main

import (
	"context"
	"time"
)

// Service registration returns before the listener and HTTPS proxy are ready.
// Only an authenticated response is success; a generic /health page from some
// other server must never make setup report a reachable private gateway.
func awaitRelayGateway(parent context.Context, c relayConfig, token string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var status struct {
		Proxy string `json:"proxy_mode"`
	}
	for {
		err := relayPost(ctx, c.URL, "/status", map[string]string{"id": c.MachineID, "token": token}, &status)
		if err == nil {
			return status.Proxy, nil
		}
		select {
		case <-ctx.Done():
			return "", err
		case <-ticker.C:
		}
	}
}
