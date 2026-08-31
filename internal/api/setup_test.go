package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func setupServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{Base: srv.URL, Token: "plt_test", HTTP: srv.Client()}
}

// The GET decodes the endpoint's shape as it actually is — including the steps' camelCase keys,
// which are the ones a Go struct silently gets wrong.
func TestSetupDecodesTheEndpointShape(t *testing.T) {
	c := setupServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/setup" {
			t.Errorf("want GET /api/v1/setup, got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"settings":{"instance_name":"acme","allow_signups":true,"setup_completed_at":"2026-08-01T00:00:00Z"},
		  "steps":[{"id":"identity","title":"Identity provider","state":"done","detail":"","envOnly":true},
		           {"id":"project","title":"Register a project","state":"blocked","blockedBy":"machine","detail":"run it"}],
		  "next":{"id":"project","title":"Register a project","state":"blocked"},"complete":false}`))
	})
	st, err := c.Setup()
	if err != nil {
		t.Fatal(err)
	}
	if st.Settings.InstanceName != "acme" || !st.Settings.AllowSignups || st.Complete {
		t.Fatalf("settings decoded wrong: %+v", st.Settings)
	}
	if !st.Steps[0].EnvOnly {
		t.Errorf("envOnly did not decode (camelCase key): %+v", st.Steps[0])
	}
	if st.Steps[1].BlockedBy != "machine" {
		t.Errorf("blockedBy did not decode (camelCase key): %+v", st.Steps[1])
	}
	if st.Next == nil || st.Next.ID != "project" {
		t.Errorf("next did not decode: %+v", st.Next)
	}
}

// 401 and 403 are DIFFERENT facts with different fixes — "log in" versus "you are not the operator"
// — so they must not collapse into one error the way the run reads deliberately do.
func TestSetupDistinguishesUnauthenticatedFromForbidden(t *testing.T) {
	for code, want := range map[int]error{401: ErrSetupNotSignedIn, 403: ErrSetupForbidden} {
		c := setupServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		})
		name := "n"
		if _, err := c.UpdateSetup(&name, nil); !errors.Is(err, want) {
			t.Errorf("status %d: want %v, got %v", code, want, err)
		}
	}
}

// An empty patch is refused HERE, before a request: the endpoint would answer 400 "nothing to
// change", and paying a round trip to be told what the caller already knew is noise.
func TestUpdateSetupRefusesAnEmptyPatchWithoutRequesting(t *testing.T) {
	hits := 0
	c := setupServer(t, func(w http.ResponseWriter, _ *http.Request) { hits++ })
	if _, err := c.UpdateSetup(nil, nil); !errors.Is(err, ErrSetupNothingToChange) {
		t.Errorf("want ErrSetupNothingToChange, got %v", err)
	}
	if hits != 0 {
		t.Fatalf("an empty patch reached the network (%d requests)", hits)
	}
}

// Pointers, so an absent field is absent from the body. A plain bool would send allow_signups:false
// on every call that only meant to rename the instance.
func TestUpdateSetupSendsOnlyWhatItWasGiven(t *testing.T) {
	var body map[string]any
	c := setupServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"settings":{"instance_name":"acme","allow_signups":false}}`))
	})
	no := false
	if _, err := c.UpdateSetup(nil, &no); err != nil {
		t.Fatal(err)
	}
	if _, present := body["instance_name"]; present {
		t.Errorf("an absent name must not be in the body: %#v", body)
	}
	if v, ok := body["allow_signups"].(bool); !ok || v {
		t.Errorf("allow_signups:false must be sent as false: %#v", body)
	}
}

// A transport failure must not echo the request back: a wrapped *url.Error carries the URL, and the
// URL of a self-hosted instance is not something to paste into an agent's transcript by accident.
func TestSetupErrorsDoNotEchoTheRequest(t *testing.T) {
	c := &Client{Base: "http://127.0.0.1:1", Token: "plt_secret_token", HTTP: http.DefaultClient}
	_, err := c.Setup()
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "plt_secret_token") {
		t.Errorf("the error echoed the request: %v", err)
	}
}

// A 400 is the endpoint's validation text and is worth passing through — flattened to one line and
// bounded, so nothing in it can pose as a section of the output it lands in.
func TestSetupPassesThroughBoundedValidationText(t *testing.T) {
	c := setupServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"instance_name must be 1-60 characters` + "\\n\\n" + strings.Repeat("x", 400) + `"}`))
	})
	name := "n"
	_, err := c.UpdateSetup(&name, nil)
	if err == nil || !strings.Contains(err.Error(), "instance_name must be") {
		t.Fatalf("want the validation text, got %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("validation text must be a single line: %q", err.Error())
	}
	if len([]rune(err.Error())) > 210 {
		t.Errorf("validation text must be bounded, got %d runes", len([]rune(err.Error())))
	}
}
