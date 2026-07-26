package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The Planning agent must be discoverable under its real name (with describe kept as the legacy
// alias), and the bulk plan_file_tree tool must be registered — the CLI half of "planning agent
// everywhere" (#573).
func TestPlanningAgentSurfaceRegistered(t *testing.T) {
	names := make([]string, 0, len(cgPromptDefs))
	for _, p := range cgPromptDefs {
		names = append(names, p["name"].(string))
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"planning_agent", "describe"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("prompt %q not registered (have: %s)", want, joined)
		}
	}
	toolNames := make([]string, 0, len(cgToolDefs))
	for _, d := range cgToolDefs {
		toolNames = append(toolNames, d["name"].(string))
	}
	if !strings.Contains(strings.Join(toolNames, ","), "plan_file_tree") {
		t.Fatalf("plan_file_tree tool not registered (have: %v)", toolNames)
	}
}

// plan_file_tree must fail fast, with an actionable message, on a rootless call — BEFORE any
// network I/O (the server-side validation handles the rest and its errors pass through verbatim).
func TestPlanFileTreeValidatesRoot(t *testing.T) {
	s := &cgServer{c: api.New(), thread: "t-1"}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	params, _ := json.Marshal(map[string]any{
		"name":      "plan_file_tree",
		"arguments": map[string]any{"root": map[string]any{"title": "", "kind": ""}},
	})
	s.handleCall(enc, rpcReq{ID: json.RawMessage(`1`), Params: params})
	if !strings.Contains(out.String(), "needs a `root`") {
		t.Fatalf("expected the actionable validation error, got: %s", out.String())
	}
}

// Zero-config MCP: the server boots in every session, so the thread must resolve LAZILY — a repo
// bound AFTER the session opened (the exact race the founder hit live: restart at 20:57, bind at
// 21:00) is picked up at the next call, with no restart. Once resolved it stays resolved.
func TestResolveThreadLazily(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no login token → markConnected stays a no-op (no network)
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	t.Chdir(repo)

	s := &cgServer{c: api.New()}
	if s.resolveThread(); s.thread != "" {
		t.Fatalf("no bind yet — must stay threadless, got %q", s.thread)
	}
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"thread":"t-lazy-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.resolveThread(); s.thread != "t-lazy-1" {
		t.Fatalf("bind written after boot must be picked up, got %q", s.thread)
	}
	// Cached: a later bind change never re-points a live session mid-conversation.
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"thread":"t-other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.resolveThread(); s.thread != "t-lazy-1" {
		t.Fatalf("resolved thread must stay stable for the session, got %q", s.thread)
	}
}

// ---- ask_peer → check_consult: the reply-delivery handoff -------------------

// consultTestServer stands in for the control plane: one POST to open a consult, then GET returns
// whatever status the test wants. Also plants a token so the tools don't short-circuit on "not signed in".
func consultTestServer(t *testing.T, get func(w http.ResponseWriter, id string)) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".partyline", "token"), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/daemon/consult":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"consult_id":"c-42"}`))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/daemon/consult/"):
			w.Header().Set("Content-Type", "application/json")
			get(w, strings.TrimPrefix(r.URL.Path, "/api/v1/daemon/consult/"))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PARTYLINE_API", srv.URL)
}

func callTool(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	s := &cgServer{c: api.New()}
	var out bytes.Buffer
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	s.handleCall(json.NewEncoder(&out), rpcReq{ID: json.RawMessage(`1`), Params: params})
	return out.String()
}

// check_consult has to EXIST and be discoverable, or the ceiling text below points at nothing.
func TestCheckConsultToolRegistered(t *testing.T) {
	names := make([]string, 0, len(cgToolDefs))
	for _, d := range cgToolDefs {
		names = append(names, d["name"].(string))
	}
	if !strings.Contains(strings.Join(names, ","), "check_consult") {
		t.Fatalf("check_consult not registered (have: %v)", names)
	}
}

// THE HOLE THIS CLOSES: when ask_peer gives up waiting, the answer is still coming, so the result
// must name the consult id AND tell the model to collect it with check_consult. A bare "still
// pending" left the reply undeliverable.
func TestAskPeerCeilingPointsAtCheckConsult(t *testing.T) {
	consultTestServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})
	old, oldP := consultPollCeiling, consultPollInterval
	consultPollCeiling, consultPollInterval = 0, time.Millisecond
	defer func() { consultPollCeiling, consultPollInterval = old, oldP }()

	got := callTool(t, "ask_peer", map[string]any{"target": "d-1", "project_label": "web", "question": "ok?"})
	for _, want := range []string{"check_consult", "c-42", "DO NOT re-ask"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ceiling result must mention %q; got: %s", want, got)
		}
	}
}

// The answer, once it lands, comes back through check_consult with the same untrusted-data framing
// ask_peer uses — the model must not be able to tell which call collected it.
func TestCheckConsultReturnsTheAnswer(t *testing.T) {
	consultTestServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(`{"status":"answered","answer":"your callers break"}`))
	})
	got := callTool(t, "check_consult", map[string]any{"consult_id": "c-42"})
	if !strings.Contains(got, "your callers break") || !strings.Contains(got, "untrusted") {
		t.Fatalf("expected the answer framed as untrusted, got: %s", got)
	}
}

// Before the answer lands: a sane waiting state that repeats the id and discourages a tight loop.
func TestCheckConsultWaitingState(t *testing.T) {
	consultTestServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})
	got := callTool(t, "check_consult", map[string]any{"consult_id": "c-42"})
	for _, want := range []string{"Still waiting", "c-42", "Don't loop"} {
		if !strings.Contains(got, want) {
			t.Fatalf("waiting result must mention %q; got: %s", want, got)
		}
	}
}

// A consult that isn't the caller's must not be distinguishable from one that never existed: the
// endpoint's ownership wall answers 403 vs 404, and this tool must flatten both to ONE sentence.
// Otherwise check_consult becomes an oracle for "does this consult id exist".
func TestCheckConsultDoesNotLeakExistence(t *testing.T) {
	consultTestServer(t, func(w http.ResponseWriter, id string) {
		if id == "c-forbidden" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"not your consult"}`))
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"no such consult"}`))
	})
	forbidden := callTool(t, "check_consult", map[string]any{"consult_id": "c-forbidden"})
	missing := callTool(t, "check_consult", map[string]any{"consult_id": "c-nope"})
	if forbidden != missing {
		t.Fatalf("403 and 404 must be indistinguishable:\n 403: %s\n 404: %s", forbidden, missing)
	}
	if strings.Contains(forbidden, "not your consult") || strings.Contains(forbidden, "403") {
		t.Fatalf("the refusal leaked the server's reason: %s", forbidden)
	}
}
