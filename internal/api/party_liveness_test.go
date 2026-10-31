package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// partyRoutes serves the two endpoints the probe reads, each with a caller-chosen status,
// so a test states the world as "open-only says X, any-status says Y".
func partyRoutes(t *testing.T, infoStatus, messagesStatus int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("probe sent Authorization %q, want the party token", got)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/info"):
			w.WriteHeader(infoStatus)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.WriteHeader(messagesStatus)
		default:
			t.Errorf("probe read an unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPartyProbe pins the distinction the probe exists for: only a party that is genuinely
// there-but-not-open reads as ended. Every other shape of "my token didn't work" — host down,
// hung, erroring, token unknown — reads as couldn't-check.
func TestPartyProbe(t *testing.T) {
	// Shrink the per-read bound so the timeout case costs milliseconds. Not parallel: shared var.
	orig := probeTimeout
	probeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { probeTimeout = orig })

	client := func(base string) *PartyClient {
		return &PartyClient{Base: base, ID: "p1", Token: "tok"}
	}

	t.Run("open-only read resolves means live", func(t *testing.T) {
		srv := partyRoutes(t, http.StatusOK, http.StatusOK)
		if got := client(srv.URL).ProbeLiveness(); got != PartyLive {
			t.Errorf("ProbeLiveness = %v, want live", got)
		}
	})

	t.Run("only the any-status read resolves means ended", func(t *testing.T) {
		srv := partyRoutes(t, http.StatusUnauthorized, http.StatusOK)
		if got := client(srv.URL).ProbeLiveness(); got != PartyEnded {
			t.Errorf("ProbeLiveness = %v, want ended", got)
		}
	})

	t.Run("unreachable host is never ended", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		base := dead.URL
		dead.Close() // nothing is listening now — a laptop off the network
		if got := client(base).ProbeLiveness(); got != PartyUnreachable {
			t.Errorf("ProbeLiveness against a dead host = %v, want unreachable", got)
		}
	})

	t.Run("timeout is never ended", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(probeTimeout * 4)
		}))
		defer srv.Close()
		if got := client(srv.URL).ProbeLiveness(); got != PartyUnreachable {
			t.Errorf("ProbeLiveness against a hung host = %v, want unreachable", got)
		}
	})

	t.Run("5xx on the open-only read is never ended", func(t *testing.T) {
		srv := partyRoutes(t, http.StatusBadGateway, http.StatusOK)
		if got := client(srv.URL).ProbeLiveness(); got != PartyUnreachable {
			t.Errorf("ProbeLiveness = %v, want unreachable", got)
		}
	})

	t.Run("5xx on the any-status read is never ended", func(t *testing.T) {
		srv := partyRoutes(t, http.StatusUnauthorized, http.StatusInternalServerError)
		if got := client(srv.URL).ProbeLiveness(); got != PartyUnreachable {
			t.Errorf("ProbeLiveness = %v, want unreachable", got)
		}
	})

	t.Run("rate limiting is never ended", func(t *testing.T) {
		srv := partyRoutes(t, http.StatusTooManyRequests, http.StatusOK)
		if got := client(srv.URL).ProbeLiveness(); got != PartyUnreachable {
			t.Errorf("ProbeLiveness = %v, want unreachable", got)
		}
	})

	t.Run("a token that resolves nowhere is not ended", func(t *testing.T) {
		// Both reads refuse: revoked, or a party that never existed. We have no evidence a
		// party ended, so we must not claim one did.
		srv := partyRoutes(t, http.StatusUnauthorized, http.StatusUnauthorized)
		if got := client(srv.URL).ProbeLiveness(); got != PartyUnreachable {
			t.Errorf("ProbeLiveness = %v, want unreachable", got)
		}
	})

	t.Run("a failed open-only read does not pay for the second", func(t *testing.T) {
		var reads int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reads++
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		if got := client(srv.URL).ProbeLiveness(); got != PartyUnreachable {
			t.Errorf("ProbeLiveness = %v, want unreachable", got)
		}
		if reads != 1 {
			t.Errorf("reads = %d, want 1 (short-circuit: the second read could mean nothing)", reads)
		}
	})

	t.Run("states have distinct names", func(t *testing.T) {
		if PartyLive.String() != "live" || PartyEnded.String() != "ended" || PartyUnreachable.String() != "unreachable" {
			t.Errorf("names = %q/%q/%q", PartyLive, PartyEnded, PartyUnreachable)
		}
		var zero PartyLiveness
		if zero != PartyUnreachable {
			t.Errorf("zero value is %v; it must be unreachable so a missed assignment cannot say ended", zero)
		}
	})
}
