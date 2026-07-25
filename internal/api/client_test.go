package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// flaky wraps a handler so its first failN requests answer 500 (a transient failure)
// and every request after that is served by ok. Lets a single test server prove the
// "5xx twice then succeed" retry path and count total hits.
func flaky(failN int32, ok http.HandlerFunc) (http.HandlerFunc, *int32) {
	var hits int32
	h := func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) <= failN {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		ok(w, r)
	}
	return h, &hits
}

// TestPartyReadRetries proves the doc/plan reads and the human-gated doc edit recover from
// transient 5xx failures (2 fails then success → no error), while plan WRITES stay single-shot
// (fail on the first 5xx, exactly one request). 4xx is never retried on any method.
func TestPartyReadRetries(t *testing.T) {
	t.Run("GetDoc retries then succeeds", func(t *testing.T) {
		t.Parallel()
		h, hits := flaky(2, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"body": "hello", "version": 7})
		})
		srv := httptest.NewServer(h)
		defer srv.Close()
		pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
		body, ver, err := pc.GetDoc()
		if err != nil {
			t.Fatalf("GetDoc after 2 transient fails: %v", err)
		}
		if body != "hello" || ver != 7 {
			t.Errorf("GetDoc = (%q, %d), want (hello, 7)", body, ver)
		}
		if *hits != 3 {
			t.Errorf("hits = %d, want 3 (2 fails + 1 success)", *hits)
		}
	})

	t.Run("ProposeEdit retries then succeeds", func(t *testing.T) {
		t.Parallel()
		h, hits := flaky(2, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
		})
		srv := httptest.NewServer(h)
		defer srv.Close()
		pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
		id, err := pc.ProposeEdit("alice", "Design", "new text")
		if err != nil {
			t.Fatalf("ProposeEdit after 2 transient fails: %v", err)
		}
		if id != 42 {
			t.Errorf("ProposeEdit id = %d, want 42", id)
		}
		if *hits != 3 {
			t.Errorf("hits = %d, want 3", *hits)
		}
	})

	t.Run("Transcript retries then succeeds", func(t *testing.T) {
		t.Parallel()
		h, hits := flaky(2, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("# transcript"))
		})
		srv := httptest.NewServer(h)
		defer srv.Close()
		pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
		md, err := pc.Transcript()
		if err != nil {
			t.Fatalf("Transcript after 2 transient fails: %v", err)
		}
		if md != "# transcript" {
			t.Errorf("Transcript = %q, want '# transcript'", md)
		}
		if *hits != 3 {
			t.Errorf("hits = %d, want 3", *hits)
		}
	})

	t.Run("PlanRead retries then succeeds", func(t *testing.T) {
		t.Parallel()
		h, hits := flaky(2, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("PlanRead method = %s, want GET", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"thread_id": "t1", "thread_title": "T", "tree": []any{}})
		})
		srv := httptest.NewServer(h)
		defer srv.Close()
		pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
		tree, err := pc.PlanRead()
		if err != nil {
			t.Fatalf("PlanRead after 2 transient fails: %v", err)
		}
		if tree.ThreadID != "t1" {
			t.Errorf("PlanRead thread = %q, want t1", tree.ThreadID)
		}
		if *hits != 3 {
			t.Errorf("hits = %d, want 3", *hits)
		}
	})

	t.Run("Recent retries then succeeds", func(t *testing.T) {
		t.Parallel()
		h, hits := flaky(2, func(w http.ResponseWriter, _ *http.Request) {
			// Emulate the SSE stream: one backlog message then the ": ready" marker.
			_, _ = w.Write([]byte("data: {\"id\":1,\"sender\":\"agent:alice\",\"body\":\"hi\"}\n"))
			_, _ = w.Write([]byte(": ready\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		})
		srv := httptest.NewServer(h)
		defer srv.Close()
		pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
		msgs, err := pc.Recent(context.Background(), "bob")
		if err != nil {
			t.Fatalf("Recent after 2 transient fails: %v", err)
		}
		if len(msgs) != 1 || msgs[0].Body != "hi" {
			t.Errorf("Recent = %+v, want one message with body 'hi'", msgs)
		}
		if *hits != 3 {
			t.Errorf("hits = %d, want 3", *hits)
		}
	})

	// Plan WRITES are not idempotent-safe — one 5xx fails immediately, single request only.
	for _, tc := range []struct {
		name string
		call func(pc *PartyClient) error
	}{
		{"plan_upsert", func(pc *PartyClient) error { _, err := pc.PlanCreateItem(map[string]any{"kind": "task", "title": "x"}); return err }},
		{"plan_move", func(pc *PartyClient) error { return pc.PlanMove("i1", nil, nil) }},
		{"plan_propose", func(pc *PartyClient) error { return pc.PlanPropose("promote", "i1", "note") }},
	} {
		tc := tc
		t.Run(tc.name+" is single-shot on 5xx", func(t *testing.T) {
			t.Parallel()
			h, hits := flaky(99, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "i1"})
			})
			srv := httptest.NewServer(h)
			defer srv.Close()
			pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
			if err := tc.call(pc); err == nil {
				t.Fatalf("%s: want error on first 5xx, got nil", tc.name)
			}
			if *hits != 1 {
				t.Errorf("%s hits = %d, want 1 (no retry on plan write)", tc.name, *hits)
			}
		})
	}

	// 4xx is a client error — never retried, surfaced immediately, on every method.
	for _, tc := range []struct {
		name string
		call func(pc *PartyClient) error
	}{
		{"GetDoc", func(pc *PartyClient) error { _, _, err := pc.GetDoc(); return err }},
		{"ProposeEdit", func(pc *PartyClient) error { _, err := pc.ProposeEdit("a", "s", "b"); return err }},
		{"Transcript", func(pc *PartyClient) error { _, err := pc.Transcript(); return err }},
		{"PlanRead", func(pc *PartyClient) error { _, err := pc.PlanRead(); return err }},
	} {
		tc := tc
		t.Run(tc.name+" does not retry 4xx", func(t *testing.T) {
			t.Parallel()
			var hits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&hits, 1)
				http.Error(w, "nope", http.StatusBadRequest)
			}))
			defer srv.Close()
			pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
			if err := tc.call(pc); err == nil {
				t.Fatalf("%s: want error on 4xx, got nil", tc.name)
			}
			if got := atomic.LoadInt32(&hits); got != 1 {
				t.Errorf("%s hits = %d, want 1 (4xx not retried)", tc.name, got)
			}
		})
	}
}

// TestPostBodyUnchanged pins postBody's chat-post behavior after the retry extraction:
// 5xx is retried (2 fails then success), a 4xx surfaces the server's error body immediately
// with no retry.
func TestPostBodyUnchanged(t *testing.T) {
	t.Run("retries 5xx then posts", func(t *testing.T) {
		t.Parallel()
		h, hits := flaky(2, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/post") {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 5})
		})
		srv := httptest.NewServer(h)
		defer srv.Close()
		pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
		id, err := pc.Post("alice", "hello", "msg")
		if err != nil {
			t.Fatalf("Post after 2 transient fails: %v", err)
		}
		if id != 5 {
			t.Errorf("Post id = %d, want 5", id)
		}
		if *hits != 3 {
			t.Errorf("hits = %d, want 3", *hits)
		}
	})

	t.Run("surfaces 4xx error body without retry", func(t *testing.T) {
		t.Parallel()
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "you cannot type yet"})
		}))
		defer srv.Close()
		pc := &PartyClient{Base: srv.URL, ID: "p1", Token: "tok"}
		_, err := pc.Post("alice", "hello", "msg")
		if err == nil || err.Error() != "you cannot type yet" {
			t.Fatalf("Post 4xx err = %v, want 'you cannot type yet'", err)
		}
		if got := atomic.LoadInt32(&hits); got != 1 {
			t.Errorf("hits = %d, want 1 (4xx not retried)", got)
		}
	})
}

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
