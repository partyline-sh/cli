package relayserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestOnlyGatewayOwnerCanIssueMachineInvites(t *testing.T) {
	s, host := fixture(t)
	post := func(path string, body map[string]string, status int) map[string]json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(host.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("%s: HTTP %d, want %d", path, response.StatusCode, status)
		}
		if status != http.StatusOK {
			return nil
		}
		var result map[string]json.RawMessage
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	field := func(body map[string]json.RawMessage, name string) string {
		t.Helper()
		var value string
		if err := json.Unmarshal(body[name], &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	owner := map[string]string{"id": fixtureID, "token": fixtureToken}
	invite := field(post("/invite", owner, http.StatusOK), "invite")
	memberID := strings.Repeat("ab", 16)
	memberToken := field(post("/join", map[string]string{"id": memberID, "invite": invite}, http.StatusOK), "token")
	member := map[string]string{"id": memberID, "token": memberToken}
	post("/status", member, http.StatusOK)

	// Membership must not delegate the owner's authority to enroll more machines.
	post("/invite", member, http.StatusForbidden)
	var outstanding int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM invites").Scan(&outstanding); err != nil || outstanding != 0 {
		t.Fatalf("member created an invitation: count=%d error=%v", outstanding, err)
	}

	// Members can still pair phones to their own computer.
	phone := map[string]string{"id": memberID, "token": memberToken, "phone": strings.Repeat("cd", 16)}
	post("/device-token", phone, http.StatusOK)
	post("/device-invite", map[string]string{"id": memberID, "token": memberToken,
		"hash": strings.Repeat("ef", 32), "route": strings.Repeat("12", 16)}, http.StatusOK)

	// The owner's explicit invitation remains a supported enrollment path.
	invite = field(post("/invite", owner, http.StatusOK), "invite")
	post("/join", map[string]string{"id": strings.Repeat("34", 16), "invite": invite}, http.StatusOK)
}
