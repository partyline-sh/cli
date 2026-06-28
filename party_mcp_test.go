package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// drive feeds newline-delimited JSON-RPC requests through the server and returns the
// decoded responses, keyed by id. Notifications (no id) produce no response.
func drive(t *testing.T, reqs ...string) map[float64]map[string]any {
	t.Helper()
	var out bytes.Buffer
	s := &mcpServer{pc: nil, name: "scribe"} // pc unused: these cases don't reach a tool body
	s.serve(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out)

	resps := map[float64]map[string]any{}
	sc := bufio.NewScanner(&out)
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
	return resps
}

func TestMCPInitializeEchoesProtocolVersion(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	res, ok := r[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %#v", r[1])
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want echoed 2025-06-18", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("tools capability not advertised: %#v", res["capabilities"])
	}
}

func TestMCPToolsListAdvertisesAllTools(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools, _ := r[2]["result"].(map[string]any)["tools"].([]any)
	got := map[string]bool{}
	for _, tn := range tools {
		got[tn.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"read_channel", "read_transcript", "post", "who", "help", "read_doc", "propose_edit", "ask_human"} {
		if !got[want] {
			t.Errorf("tools/list missing %q (got %v)", want, got)
		}
	}
}

func TestMCPPostRejectsEmptyMessage(t *testing.T) {
	// Empty message must fail with isError BEFORE any API call (pc is nil here, so
	// reaching the API would panic — proving validation short-circuits).
	r := drive(t, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"post","arguments":{"message":"  "}}}`)
	res, ok := r[5]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %#v", r[5])
	}
	if res["isError"] != true {
		t.Errorf("expected isError for empty message, got %#v", res)
	}
}

func TestMCPProposeEditRejectsMissingArgs(t *testing.T) {
	// Missing section/new_body must fail with isError BEFORE any API call (pc is nil here,
	// so reaching the API would panic — proving validation short-circuits).
	r := drive(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"propose_edit","arguments":{"section":"Risks"}}}`)
	res, ok := r[3]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %#v", r[3])
	}
	if res["isError"] != true {
		t.Errorf("expected isError for missing new_body, got %#v", res)
	}
}

func TestMCPUnknownMethodErrors(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":4,"method":"does/not/exist"}`)
	if _, ok := r[4]["error"]; !ok {
		t.Errorf("expected JSON-RPC error for unknown method, got %#v", r[4])
	}
}

func TestMCPNotificationGetsNoReply(t *testing.T) {
	// A request with no id (notification) must produce no response at all.
	r := drive(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(r) != 0 {
		t.Errorf("notification produced a response: %#v", r)
	}
}
