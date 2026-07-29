package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The run id must be validated in the CLIENT too, not only at the MCP boundary — so no caller can
// build a request path from unchecked input. A rejection must cost ZERO requests.
func TestGetRunRejectsNonUUIDWithoutRequesting(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Token: "plt_test", HTTP: srv.Client()}

	for _, bad := range []string{"", "abc", "../me", "3f1a2b4c-5d6e-7f80-9012-3456789abcde/logs", "3f1a2b4c5d6e7f8090123456789abcde"} {
		if _, err := c.GetRun(bad); !errors.Is(err, ErrBadRunID) {
			t.Errorf("GetRun(%q): want ErrBadRunID, got %v", bad, err)
		}
		if _, err := c.GetRunLogs(bad); !errors.Is(err, ErrBadRunID) {
			t.Errorf("GetRunLogs(%q): want ErrBadRunID, got %v", bad, err)
		}
	}
	if hits != 0 {
		t.Fatalf("a rejected id still reached the network (%d requests)", hits)
	}
}

// 401 / 403 / 404 must be INDISTINGUISHABLE to the caller: telling apart "not yours" from "doesn't
// exist" would confirm the existence of another org's run id. The error must also never echo the
// request (no URL, no token).
func TestGetRunCollapsesAuthAndMissing(t *testing.T) {
	const id = "3f1a2b4c-5d6e-7f80-9012-3456789abcde"
	for _, code := range []int{401, 403, 404} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"run not found"}`))
		}))
		c := &Client{Base: srv.URL, Token: "plt_super_secret_value", HTTP: srv.Client()}
		_, err := c.GetRun(id)
		if !errors.Is(err, ErrRunNotVisible) {
			t.Errorf("status %d: want ErrRunNotVisible, got %v", code, err)
		}
		if _, err := c.GetRunLogs(id); !errors.Is(err, ErrRunNotVisible) {
			t.Errorf("status %d (logs): want ErrRunNotVisible, got %v", code, err)
		}
		if strings.Contains(err.Error(), "plt_") || strings.Contains(err.Error(), srv.URL) {
			t.Errorf("status %d: the error leaked the token or the request URL: %v", code, err)
		}
		srv.Close()
	}
}

// The GETs must be GETs, hit the id's own path, carry the token ONLY in the Authorization header,
// and send no query string — the "a model cannot steer this at another endpoint" invariant.
func TestGetRunIsAReadOnlyGet(t *testing.T) {
	const id = "3f1a2b4c-5d6e-7f80-9012-3456789abcde"
	var gotMethod, gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery, gotAuth = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"run":{"id":"` + id + `","status":"done"},"tasks":[],"logs":[]}`))
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL, Token: "plt_tok", HTTP: srv.Client()}

	snap, err := c.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Run.Status != "done" {
		t.Errorf("decode failed: %+v", snap.Run)
	}
	if gotMethod != "GET" || gotPath != "/api/v1/runs/"+id || gotQuery != "" {
		t.Errorf("want a bare GET of the run's path, got %s %s?%s", gotMethod, gotPath, gotQuery)
	}
	if gotAuth != "Bearer plt_tok" {
		t.Errorf("token must travel in the Authorization header only, got %q", gotAuth)
	}
	if _, err := c.GetRunLogs(id); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || gotPath != "/api/v1/runs/"+id+"/logs" || gotQuery != "" {
		t.Errorf("want a bare GET of the logs path, got %s %s?%s", gotMethod, gotPath, gotQuery)
	}
}
