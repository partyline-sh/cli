package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewaySetupWaitsForAuthenticatedStartup(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" || r.Method != "POST" {
			t.Error("setup did not request authenticated status")
		}
		if calls.Add(1) < 3 {
			http.Error(w, "starting", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"proxy_mode":"forwarded"}`))
	}))
	defer s.Close()
	old := relayHTTP
	relayHTTP = s.Client()
	defer func() { relayHTTP = old }()
	mode, err := awaitRelayGateway(context.Background(), relayConfig{URL: s.URL}, "token", 2*time.Second)
	if err != nil || mode != "forwarded" || calls.Load() != 3 {
		t.Fatal("startup retry failed", mode, err, calls.Load())
	}
}

func TestGatewaySetupNeverAcceptsUnauthenticatedHealthPage(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Error(w, "invalid owner", 403)
	}))
	defer s.Close()
	old := relayHTTP
	relayHTTP = s.Client()
	defer func() { relayHTTP = old }()
	if _, err := awaitRelayGateway(context.Background(), relayConfig{URL: s.URL}, "wrong", 10*time.Millisecond); err == nil {
		t.Fatal("gateway accepted without authenticated status")
	}
}
