package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ClaimNextTask (#77 slice 1) has one real branch to prove: a drained pool returns (nil, nil) so
// the worker stops looping, while an available task decodes into a *ClaimedTask. Both come back
// as HTTP 200 with a { "task": ... } body (never 204 — daemonDo decodes the body).
func TestClaimNextTask(t *testing.T) {
	t.Run("claims a task", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/api/v1/daemon/run/run-1/claim" {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("auth header = %q, want Bearer tok", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"task": map[string]any{"idx": 3, "task": "do the thing", "lease_expires_at": "2026-07-04T00:00:00Z"},
			})
		}))
		defer srv.Close()

		got, err := ClaimNextTask(srv.URL, "tok", "run-1", 3600)
		if err != nil {
			t.Fatalf("ClaimNextTask: %v", err)
		}
		if got == nil {
			t.Fatal("got nil, want a claimed task")
		}
		if got.Idx != 3 || got.Task != "do the thing" || got.LeaseExpires != "2026-07-04T00:00:00Z" {
			t.Errorf("claimed = %+v, want {3, \"do the thing\", 2026-07-04T00:00:00Z}", *got)
		}
	})

	t.Run("drained pool returns nil, nil", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"task": nil})
		}))
		defer srv.Close()

		got, err := ClaimNextTask(srv.URL, "tok", "run-1", 3600)
		if err != nil {
			t.Fatalf("ClaimNextTask: %v", err)
		}
		if got != nil {
			t.Errorf("drained pool = %+v, want nil (worker's stop signal)", *got)
		}
	})
}
