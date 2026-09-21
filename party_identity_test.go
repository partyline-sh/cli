package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// TestHumanPartyPostIdentity pins the CREDENTIAL a person's CLI party message goes out with —
// the only thing that decides whether the party reads them as `user:<display name>` or
// `agent:<name>`, since the server picks the prefix from the auth mode.
func TestHumanPartyPostIdentity(t *testing.T) {
	const partyTok = "plt_pty_party-scoped"
	const loginTok = "plt_the-humans-login"

	// postServer records the Authorization on every post, and refuses the login token when
	// rejectLogin is set (the "signed in but expired/revoked" case).
	newServer := func(t *testing.T, rejectLogin bool, seen *[]string) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/post") {
				http.NotFound(w, r)
				return
			}
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			*seen = append(*seen, auth)
			if rejectLogin && auth == loginTok {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":"unauthenticated"}`)
				return
			}
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["body"] != "hello room" {
				t.Errorf("body = %v, want the message the human typed", in["body"])
			}
			_, _ = io.WriteString(w, `{"id":7}`)
		}))
		t.Cleanup(srv.Close)
		t.Setenv("PARTYLINE_API", srv.URL) // the party lives on the plane this CLI is logged in to
		return srv
	}

	t.Run("a usable login token posts as the human", func(t *testing.T) {
		var seen []string
		srv := newServer(t, false, &seen)
		var out bytes.Buffer
		h := newHumanPoster(&api.PartyClient{Base: srv.URL, ID: "p1", Token: partyTok}, "Darcy-Reno", loginTok, &out)

		if _, err := h.post("hello room", "msg"); err != nil {
			t.Fatalf("post: %v", err)
		}
		if len(seen) != 1 || seen[0] != loginTok {
			t.Errorf("credentials used = %v, want one post under the login token", seen)
		}
		if out.Len() != 0 {
			t.Errorf("unexpected notice: %q", out.String())
		}
	})

	t.Run("no login token falls back to the party token and says so", func(t *testing.T) {
		var seen []string
		srv := newServer(t, false, &seen)
		var out bytes.Buffer
		h := newHumanPoster(&api.PartyClient{Base: srv.URL, ID: "p1", Token: partyTok}, "Darcy-Reno", "", &out)

		if _, err := h.post("hello room", "msg"); err != nil {
			t.Fatalf("post: %v", err) // nothing the human typed may be lost
		}
		if len(seen) != 1 || seen[0] != partyTok {
			t.Errorf("credentials used = %v, want one post under the party token", seen)
		}
		if !strings.Contains(out.String(), "ptln login") {
			t.Errorf("notice = %q, want it to name `ptln login`", out.String())
		}
		if strings.Contains(out.String(), partyTok) || strings.Contains(out.String(), loginTok) {
			t.Errorf("notice leaked a token: %q", out.String())
		}
	})

	t.Run("a rejected login token falls back once, then stops offering it", func(t *testing.T) {
		var seen []string
		srv := newServer(t, true, &seen)
		var out bytes.Buffer
		h := newHumanPoster(&api.PartyClient{Base: srv.URL, ID: "p1", Token: partyTok}, "Darcy-Reno", loginTok, &out)

		if _, err := h.post("hello room", "msg"); err != nil {
			t.Fatalf("first post: %v", err)
		}
		if _, err := h.post("hello room", "msg"); err != nil {
			t.Fatalf("second post: %v", err)
		}
		want := []string{loginTok, partyTok, partyTok}
		if strings.Join(seen, ",") != strings.Join(want, ",") {
			t.Errorf("credentials used = %v, want %v", seen, want)
		}
		if !strings.Contains(out.String(), "ptln login") {
			t.Errorf("notice = %q, want it to name `ptln login`", out.String())
		}
		if n := strings.Count(out.String(), "ptln login"); n != 1 {
			t.Errorf("notice printed %d times, want once per session", n)
		}
	})

	t.Run("a party on another control plane never sees the login token", func(t *testing.T) {
		var seen []string
		srv := newServer(t, false, &seen)
		t.Setenv("PARTYLINE_API", "https://partyline.sh") // logged in somewhere else
		var out bytes.Buffer
		h := newHumanPoster(&api.PartyClient{Base: srv.URL, ID: "p1", Token: partyTok}, "Darcy-Reno", loginTok, &out)

		if _, err := h.post("hello room", "msg"); err != nil {
			t.Fatalf("post: %v", err)
		}
		if len(seen) != 1 || seen[0] != partyTok {
			t.Errorf("credentials used = %v, want the party token only", seen)
		}
	})
}
