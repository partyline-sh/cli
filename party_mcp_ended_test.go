package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// The party token that MUST NOT appear in anything the server emits — the notice lands in a
// system prompt and in transcripts, and a token there outlives the party it opened.
const endedTestToken = "plt_pty_neverprintme"

// driveParty runs the REAL server (probe included) against a stub host and returns the
// responses by id plus the raw stdout, so a test can scan every byte we emitted.
func driveParty(t *testing.T, base string, reqs ...string) (map[float64]map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	s := &mcpServer{
		pc:   &api.PartyClient{Base: base, ID: "party-7f3a", Token: endedTestToken},
		name: "scribe",
	}
	s.serve(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	raw := out.String()
	resps := map[float64]map[string]any{}
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("non-JSON response line %q: %v", sc.Text(), err)
		}
		if id, ok := m["id"].(float64); ok {
			resps[id] = m
		}
	}
	return resps, raw
}

const (
	reqInit  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	reqList  = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	reqCall  = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"party_ended","arguments":{}}}`
	reqOther = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_channel","arguments":{}}}`
)

// endedHostStub answers the way a closed party does: the open-only read refuses, the
// any-status read resolves. (api.ProbeLiveness reads that pair as PartyEnded.)
func endedHostStub(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info") {
			http.Error(w, `{"error":"bad token"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"messages":[],"next":null}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func toolNames(t *testing.T, resp map[string]any) []string {
	t.Helper()
	tools, ok := resp["result"].(map[string]any)["tools"].([]any)
	if !ok {
		t.Fatalf("no tools in tools/list result: %#v", resp)
	}
	var names []string
	for _, tn := range tools {
		names = append(names, tn.(map[string]any)["name"].(string))
	}
	return names
}

func text(t *testing.T, resp map[string]any) string {
	t.Helper()
	content, ok := resp["result"].(map[string]any)["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("no content in tools/call result: %#v", resp)
	}
	return content[0].(map[string]any)["text"].(string)
}

func TestPartyMCPEnded(t *testing.T) {
	t.Run("withdraws its tools and says why", func(t *testing.T) {
		base := endedHostStub(t)
		r, raw := driveParty(t, base, reqInit, reqList, reqCall)

		instructions, _ := r[1]["result"].(map[string]any)["instructions"].(string)
		if instructions == "" {
			t.Fatalf("initialize carried no instructions: %#v", r[1])
		}

		names := toolNames(t, r[2])
		if len(names) != 1 {
			t.Errorf("offered %d tools, want exactly 1: %v", len(names), names)
		}

		// The two surfaces must say the SAME thing — one string, not two texts that drift.
		tools := r[2]["result"].(map[string]any)["tools"].([]any)
		if desc := tools[0].(map[string]any)["description"].(string); desc != instructions {
			t.Errorf("tool description differs from instructions:\n tool: %q\n inst: %q", desc, instructions)
		}
		if got := text(t, r[3]); got != instructions {
			t.Errorf("tool call differs from instructions:\n call: %q\n inst: %q", got, instructions)
		}

		// Names the party, names the host, and prints the command that removes the
		// registration — everything a reader needs to act without asking anyone.
		for _, want := range []string{"ended", "party-7f3a", endedHost(base), "claude mcp remove partyline-party"} {
			if !strings.Contains(instructions, want) {
				t.Errorf("statement does not mention %q:\n%s", want, instructions)
			}
		}
		if strings.Contains(raw, endedTestToken) {
			t.Errorf("the party token appears in the server's output:\n%s", raw)
		}
	})

	// Unknown is deliberately treated as live: a probe that could not answer must never cost
	// a working session its tools.
	for _, tc := range []struct {
		name string
		base func(t *testing.T) string
	}{
		{"a 5xx host changes nothing", func(t *testing.T) string {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusBadGateway)
			}))
			t.Cleanup(srv.Close)
			return srv.URL
		}},
		{"an unreachable host changes nothing", func(t *testing.T) string {
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			srv.Close() // nothing is listening on that port any more
			return srv.URL
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, raw := driveParty(t, tc.base(t), reqInit, reqList, reqOther)

			if _, ok := r[1]["result"].(map[string]any)["instructions"]; ok {
				t.Errorf("initialize gained instructions for an unreachable party: %#v", r[1])
			}
			if names := toolNames(t, r[2]); len(names) != len(toolDefs) {
				t.Errorf("offered %d tools, want the full %d: %v", len(names), len(toolDefs), names)
			}
			// The normal tool ran and failed against the dead stub, as it always would —
			// no ended-party statement anywhere.
			if strings.Contains(raw, "This party has ended") {
				t.Errorf("withdrew tools on an unanswered probe:\n%s", raw)
			}
			if strings.Contains(raw, endedTestToken) {
				t.Errorf("the party token appears in the server's output:\n%s", raw)
			}
		})
	}
}
