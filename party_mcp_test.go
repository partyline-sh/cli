package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
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
	for _, want := range []string{"read_channel", "read_transcript", "post", "who", "help", "read_doc", "propose_edit", "ask_human", "propose_fix", "plan_read", "plan_upsert", "plan_move", "plan_propose", "skill_list", "skill_fetch"} {
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

// planUpsertIsError drives one plan_upsert call and returns (result, isError). pc is nil in
// drive, so any case that returns without panicking proves validation short-circuited
// before the API — same technique as the post/propose_edit tests.
func planUpsertIsError(t *testing.T, args string) map[string]any {
	t.Helper()
	r := drive(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"plan_upsert","arguments":`+args+`}}`)
	res, ok := r[7]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %#v", r[7])
	}
	return res
}

func TestMCPPlanUpsertRejectsCreateWithoutKindTitle(t *testing.T) {
	// A create (no id) missing kind and/or title must fail before any API call.
	for _, args := range []string{`{}`, `{"title":"Ship it"}`, `{"kind":"task"}`, `{"kind":" ","title":" "}`} {
		if res := planUpsertIsError(t, args); res["isError"] != true {
			t.Errorf("args %s: expected isError, got %#v", args, res)
		}
	}
}

func TestMCPPlanUpsertRejectsReadinessWithoutNote(t *testing.T) {
	// Setting readiness without a readiness_note must fail client-side, on create and edit.
	for _, args := range []string{
		`{"kind":"task","title":"Ship it","readiness":3}`,
		`{"id":"itm_1","readiness":"ready","readiness_note":"  "}`,
	} {
		if res := planUpsertIsError(t, args); res["isError"] != true {
			t.Errorf("args %s: expected isError, got %#v", args, res)
		}
	}
}

func TestMCPPlanUpsertRejectsEmptyEdit(t *testing.T) {
	// An edit (id present) with nothing to change must fail before any API call.
	if res := planUpsertIsError(t, `{"id":"itm_1"}`); res["isError"] != true {
		t.Errorf("expected isError for empty edit, got %#v", res)
	}
}

func TestMCPPlanUpsertRejectsParentIDOnEdit(t *testing.T) {
	// Reparenting is plan_move's job — an edit carrying parent_id must be refused.
	if res := planUpsertIsError(t, `{"id":"itm_1","parent_id":"itm_2","title":"x"}`); res["isError"] != true {
		t.Errorf("expected isError for parent_id on edit, got %#v", res)
	}
}

func TestMCPPlanMoveRejectsMissingID(t *testing.T) {
	// Missing id must fail with isError BEFORE any API call (pc is nil).
	r := drive(t, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"plan_move","arguments":{"parent_id":"itm_2"}}}`)
	res, ok := r[8]["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %#v", r[8])
	}
	if res["isError"] != true {
		t.Errorf("expected isError for missing id, got %#v", res)
	}
}

func TestMCPPlanProposeRejectsBadArgs(t *testing.T) {
	// Unknown action or missing item_id must fail with isError BEFORE any API call (pc is nil).
	for _, args := range []string{
		`{"action":"delete","item_id":"itm_1"}`, // not promote|archive
		`{"action":"promote"}`,                  // no item_id
		`{"item_id":"itm_1"}`,                   // no action
	} {
		r := drive(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"plan_propose","arguments":%s}}`, args))
		res, ok := r[9]["result"].(map[string]any)
		if !ok {
			t.Fatalf("args %s: no result: %#v", args, r)
		}
		if res["isError"] != true {
			t.Errorf("args %s: expected isError, got %#v", args, res)
		}
	}
}

func TestMCPSkillFetchRejectsBadName(t *testing.T) {
	// An empty or malformed slug (../, uppercase, slashes, too long) must fail with isError BEFORE
	// any API call — the name is a path/injection boundary. pc is nil in drive, and skillClient's
	// network calls are never reached, so returning without panic proves validation short-circuited.
	for _, args := range []string{
		`{"name":"  "}`,            // empty after trim
		`{"name":"../etc/passwd"}`, // path traversal
		`{"name":"Deploy"}`,        // uppercase
		`{"name":"a/b"}`,           // slash
		`{"name":"-lead-hyphen"}`,  // must start alphanumeric
		`{}`,                       // missing name
	} {
		r := drive(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"skill_fetch","arguments":%s}}`, args))
		res, ok := r[11]["result"].(map[string]any)
		if !ok {
			t.Fatalf("args %s: no result: %#v", args, r)
		}
		if res["isError"] != true {
			t.Errorf("args %s: expected isError, got %#v", args, res)
		}
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
