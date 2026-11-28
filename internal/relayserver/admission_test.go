package relayserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestOneClientCannotFillUnauthenticatedSlots(t *testing.T) {
	_, host := fixture(t)
	for _, test := range []struct {
		path string
		cap  int
	}{{"/connect", 8}, {"/join-connect", 2}} {
		t.Run(test.path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			dial := func(ip string) (*websocket.Conn, *http.Response, error) {
				return websocket.Dial(ctx, strings.Replace(host.URL, "http", "ws", 1)+test.path,
					&websocket.DialOptions{HTTPHeader: http.Header{"X-Forwarded-For": []string{ip}}})
			}
			for i := 0; i < test.cap; i++ {
				c, _, err := dial("198.51.100.1")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { c.CloseNow() })
			}
			if c, response, err := dial("198.51.100.1"); err == nil {
				c.CloseNow()
				t.Fatal("one client consumed more than its pre-authentication allowance")
			} else if response == nil || response.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("wanted HTTP 429, response=%v error=%v", response, err)
			}
			c, _, err := dial("198.51.100.2")
			if err != nil {
				t.Fatalf("another client lost admission: %v", err)
			}
			c.CloseNow()
		})
	}
}

func TestDeviceInviteAllowanceIsPerMachine(t *testing.T) {
	s, host := fixture(t)
	member, memberToken := strings.Repeat("ab", 16), strings.Repeat("cd", 32)
	if _, err := s.db.Exec("INSERT INTO machines(id,hash) VALUES(?,?)", member, Digest(memberToken)); err != nil {
		t.Fatal(err)
	}
	issue := func(machine, token string, index, want int) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"id": machine, "token": token,
			"hash": fmt.Sprintf("%064x", index), "route": fmt.Sprintf("%032x", index)})
		response, err := http.Post(host.URL+"/device-invite", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("invite %d: HTTP %d, want %d", index, response.StatusCode, want)
		}
	}
	for i := 1; i <= 16; i++ {
		issue(member, memberToken, i, http.StatusOK)
	}
	issue(member, memberToken, 17, http.StatusTooManyRequests)
	issue(fixtureID, fixtureToken, 18, http.StatusOK)
	if _, err := s.db.Exec("UPDATE device_invites SET expires=0 WHERE machine=?", member); err != nil {
		t.Fatal(err)
	}
	issue(member, memberToken, 19, http.StatusOK)
}
