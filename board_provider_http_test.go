package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// board_provider_http_test.go — the HTTP transport against a real server.
//
// The bug these exist for: the catalog has always described HTTP servers (url + headers), and the
// board provider only implemented stdio. An HTTP entry was skipped with no error and no board — a
// correctly configured, correctly flagged provider simply never appeared in the switcher.

// httpProvider stands up a board-serving MCP server. `sse` chooses whether it answers as plain JSON
// or as an event stream, because a streamable-HTTP server may legitimately do either.
func httpProvider(t *testing.T, sse bool, wantAuth string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}
		case "resources/read":
			var p struct {
				URI string `json:"uri"`
			}
			_ = json.Unmarshal(req.Params, &p)
			scope := "All"
			if i := strings.Index(p.URI, "scope="); i >= 0 {
				scope = p.URI[i+6:]
			}
			doc := map[string]any{
				"scope":   scope,
				"columns": []any{map[string]any{"key": "new", "title": "New"}},
				"cards": []any{map[string]any{
					"id": "t1", "column": "new", "title": "Printer drops a line", "state": "open",
				}},
			}
			body, _ := json.Marshal(doc)
			result = map[string]any{"contents": []any{
				map[string]any{"uri": p.URI, "mimeType": "application/json", "text": string(body)},
			}}
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		env, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			// Framing a real server sends: a comment, an event line, then the data.
			fmt.Fprintf(w, ": keepalive\nevent: message\ndata: %s\n\n", env)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(env)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPProviderLoadsABoard(t *testing.T) {
	srv := httpProvider(t, false, "")
	p := providerSource{name: "odoo", url: srv.URL}

	d, err := p.Load("42")
	if err != nil {
		t.Fatal(err)
	}
	if d.Scope != "42" {
		t.Fatalf("scope = %q — the scope did not reach the server", d.Scope)
	}
	if len(d.Columns) != 1 || d.Title("new") != "New" {
		t.Fatalf("columns = %+v", d.Columns)
	}
	card, ok := d.Find("t1")
	if !ok || !card.Foreign {
		t.Fatalf("card = %+v ok=%v", card, ok)
	}
	if d.Live {
		t.Fatal("a provider board never polls")
	}
}

// A streamable-HTTP server may answer a single request with an event stream. Reading the body
// naively then fails to parse for a reason nobody can see.
func TestHTTPProviderHandlesEventStreamResponses(t *testing.T) {
	srv := httpProvider(t, true, "")
	p := providerSource{name: "odoo", url: srv.URL}

	d, err := p.Load("51")
	if err != nil {
		t.Fatalf("SSE-framed response not handled: %v", err)
	}
	if _, ok := d.Find("t1"); !ok {
		t.Fatal("the card did not survive the SSE frame")
	}
}

// The whole reason HTTP matters here: a hosted server behind a bearer token.
func TestHTTPProviderSendsItsHeaders(t *testing.T) {
	srv := httpProvider(t, false, "Bearer secret-token")
	p := providerSource{
		name:    "odoo",
		url:     srv.URL,
		headers: map[string]string{"Authorization": "Bearer secret-token"},
	}
	if _, err := p.Load(""); err != nil {
		t.Fatalf("configured credentials were not sent: %v", err)
	}

	// …and the failure when they are wrong has to name the file the operator edits.
	bad := providerSource{name: "odoo", url: srv.URL, headers: map[string]string{"Authorization": "Bearer nope"}}
	_, err := bad.Load("")
	if err == nil {
		t.Fatal("a rejected token must be an error")
	}
	if !strings.Contains(err.Error(), "mcp.json") || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("auth failure should point at the thing to fix, got %q", err)
	}
}

// Scopes come over HTTP too, so the project picker works for a hosted provider.
func TestHTTPProviderListsScopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := map[string]any{}
		if req.Method == "resources/read" {
			doc := `{"scopes":[{"id":"42","label":"ACR POS","note":"48 open"}]}`
			result = map[string]any{"contents": []any{map[string]any{"text": doc}}}
		}
		env, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(env)
	}))
	defer srv.Close()

	scopes, err := providerSource{name: "odoo", url: srv.URL}.Scopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0].Label != "ACR POS" || scopes[0].ID != "42" {
		t.Fatalf("scopes = %+v", scopes)
	}
}

// Discovery must accept BOTH catalog shapes. Skipping the url-only entry is the bug: configured,
// flagged as a board, and silently absent from the switcher.
func TestDiscoveryAcceptsBothTransports(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	cat := `{"servers":{
	  "odoo-http":  {"url":"https://example.invalid/mcp","headers":{"Authorization":"Bearer x"},"board":true},
	  "odoo-stdio": {"command":"python3","args":["x.py"],"board":true},
	  "not-a-board":{"url":"https://example.invalid/other"},
	  "empty":      {"board":true}
	}}`
	if err := os.WriteFile(filepath.Join(home, ".partyline", "mcp.json"), []byte(cat), 0o600); err != nil {
		t.Fatal(err)
	}

	got := discoverBoardProviders()
	names := map[string]bool{}
	for _, g := range got {
		names[g.Name()] = true
	}
	if !names["odoo-http"] {
		t.Error("an HTTP board provider was skipped — this is the bug")
	}
	if !names["odoo-stdio"] {
		t.Error("a stdio board provider was skipped")
	}
	if names["not-a-board"] {
		t.Error("a server that did not opt in must not be discovered")
	}
	if names["empty"] {
		t.Error("an entry describing neither transport cannot be called")
	}
	if len(got) != 2 {
		t.Fatalf("discovered %d providers, want exactly the two boards", len(got))
	}
}

// An HTTP provider that answers with junk fails the same way a stdio one does — one message for one
// problem, whichever transport it arrived over.
func TestHTTPProviderJunkIsReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := map[string]any{}
		if req.Method == "resources/read" {
			result = map[string]any{"contents": []any{map[string]any{"text": "not a board"}}}
		}
		env, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		_, _ = w.Write(env)
	}))
	defer srv.Close()

	_, err := providerSource{name: "odoo", url: srv.URL}.Load("")
	if err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("err = %v, want the same 'cannot read' message the stdio path gives", err)
	}
}
