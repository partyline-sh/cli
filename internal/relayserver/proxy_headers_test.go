package relayserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyUsesLastForwardedHeaderValue(t *testing.T) {
	proxies, err := parseTrustedProxy("")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{trustedProxies: proxies}
	for _, test := range []struct {
		name, peer, key, mode string
		values                []string
	}{
		{"appended header", "127.0.0.1:5000", "198.51.100.8", "forwarded", []string{"203.0.113.7", "198.51.100.8"}},
		{"appended list", "127.0.0.1:5000", "198.51.100.8", "forwarded", []string{"203.0.113.7", "192.0.2.1, 198.51.100.8"}},
		{"invalid final hop", "127.0.0.1:5000", "127.0.0.1", "shared", []string{"203.0.113.7", "invalid"}},
		{"empty final value", "127.0.0.1:5000", "127.0.0.1", "shared", []string{"203.0.113.7", ""}},
		{"empty final list hop", "127.0.0.1:5000", "127.0.0.1", "shared", []string{"203.0.113.7", "198.51.100.8,"}},
		{"untrusted peer", "192.0.2.10:5000", "192.0.2.10", "direct", []string{"203.0.113.7", "198.51.100.8"}},
		{"mapped IPv4", "127.0.0.1:5000", "198.51.100.8", "forwarded", []string{"203.0.113.7", "::ffff:198.51.100.8"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://relay.test/connect", nil)
			r.RemoteAddr = test.peer
			for _, value := range test.values {
				r.Header.Add("X-Forwarded-For", value)
			}
			if got := s.rateKey(r); got != test.key {
				t.Fatalf("rate key = %q, want %q", got, test.key)
			}
			if got := s.proxyMode(r); got != test.mode {
				t.Fatalf("proxy mode = %q, want %q", got, test.mode)
			}
		})
	}
}

func TestSpoofedFirstHeaderCannotRotateProxyRateBucket(t *testing.T) {
	s, _ := fixture(t)
	for i := 0; i <= 120; i++ {
		r := httptest.NewRequest(http.MethodGet, "https://relay.test/connect", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Add("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i+1))
		r.Header.Add("X-Forwarded-For", "198.51.100.8")
		if allowed := s.allowHTTP(r); allowed != (i < 120) {
			t.Fatalf("request %d allowed=%v: spoofed first header bypassed the shared client bucket", i+1, allowed)
		}
	}
}
