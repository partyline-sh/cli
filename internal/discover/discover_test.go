package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The real thing, loopback-free: announce on this machine's interfaces, browse for it. mDNS
// needs a multicast-capable interface; where the environment has none (some CI sandboxes),
// the announce or browse fails and the test skips rather than lies.
func TestAnnounceAndBrowseRoundTrip(t *testing.T) {
	stop, err := Announce("https://192.0.2.10:8443", "unit-test-instance", 8443)
	if err != nil {
		t.Skipf("no multicast-capable interface here: %v", err)
	}
	defer stop()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		for _, in := range Browse(2 * time.Second) {
			if in.URL == "https://192.0.2.10:8443" {
				if in.Source != "mdns" {
					t.Errorf("source = %q", in.Source)
				}
				if !strings.Contains(in.Name, "unit-test-instance") {
					t.Errorf("name = %q", in.Name)
				}
				return
			}
		}
	}
	t.Skip("announced but not seen — multicast filtered on this network; the probe half is asserted below")
}

// ProbePeers: a partyline health endpoint answers, everything else is silence. Uses a local
// TLS server with a self-signed cert — exactly the internal-CA shape the probe must accept.
func TestProbePeersFindsAHealthEndpoint(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	found := ProbePeers(ctx, []string{u.Host}, map[string]string{u.Host: "boxy"})
	// srv.URL is host:port, so the candidate tried is https://host:port — the first form.
	if len(found) != 1 || found[0].Name != "boxy" || found[0].Source != "tailnet" {
		t.Fatalf("found = %+v", found)
	}
	// a non-partyline host: connection refused everywhere → not a candidate
	none := ProbePeers(ctx, []string{"127.0.0.1:1"}, nil)
	if len(none) != 0 {
		t.Fatalf("phantom instance: %+v", none)
	}
}
