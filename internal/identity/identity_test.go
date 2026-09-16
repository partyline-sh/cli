package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// withTestKey swaps the baked-in assertion pubkey for a freshly generated one so
// tests can mint VALID assertions (the production private key lives in the box's .env, not
// here). Restored after the test.
func withTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	orig := assertPub
	assertPub = pub
	t.Cleanup(func() { assertPub = orig })
	return priv
}

// sign builds a `<b64url(json)>.<b64url(sig)>` assertion signed by priv.
func sign(t *testing.T, priv ed25519.PrivateKey, c Claims) string {
	t.Helper()
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	seg1 := base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(priv, []byte(seg1))
	return seg1 + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func validClaims() Claims {
	return Claims{
		Sub: "alice", Name: "Alice", Email: "alice@acme.com",
		Code: "ABC123", Access: "full",
		Iat: time.Now().Unix(), Exp: time.Now().Add(2 * time.Minute).Unix(),
	}
}

func TestVerify_ValidFullAccess(t *testing.T) {
	priv := withTestKey(t)
	got, err := Verify(sign(t, priv, validClaims()), "ABC123")
	if err != nil {
		t.Fatalf("expected valid, got error: %v", err)
	}
	if got.Sub != "alice" || got.Email != "alice@acme.com" {
		t.Errorf("claims not parsed: %+v", got)
	}
	if got.Access != "full" {
		t.Errorf("Access = %q, want full", got.Access)
	}
}

func TestVerify_ViewerAccessParsed(t *testing.T) {
	priv := withTestKey(t)
	c := validClaims()
	c.Access = "viewer"
	got, err := Verify(sign(t, priv, c), "ABC123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Access != "viewer" {
		t.Errorf("Access = %q, want viewer", got.Access)
	}
}

func TestVerify_TamperedSignatureRejected(t *testing.T) {
	priv := withTestKey(t)
	a := sign(t, priv, validClaims())
	// flip the last byte of the signature segment
	b := []byte(a)
	b[len(b)-1] ^= 0x01
	if _, err := Verify(string(b), "ABC123"); err == nil {
		t.Fatal("tampered signature accepted — must fail closed")
	}
}

func TestVerify_TamperedPayloadRejected(t *testing.T) {
	priv := withTestKey(t)
	a := sign(t, priv, validClaims())
	// keep the signature, swap the payload for one claiming full access on a
	// different identity — the sig no longer covers seg1, so it must reject.
	forged := validClaims()
	forged.Sub = "attacker"
	payload, _ := json.Marshal(forged)
	seg1 := base64.RawURLEncoding.EncodeToString(payload)
	_, seg2, _ := cut(a)
	if _, err := Verify(seg1+"."+seg2, "ABC123"); err == nil {
		t.Fatal("payload tamper with stale signature accepted — must fail closed")
	}
}

func TestVerify_WrongSessionCodeRejected(t *testing.T) {
	priv := withTestKey(t)
	// assertion is scoped to ABC123 but presented to a different session
	if _, err := Verify(sign(t, priv, validClaims()), "OTHER1"); err == nil {
		t.Fatal("assertion accepted for the wrong session code")
	}
}

func TestVerify_ExpiredRejected(t *testing.T) {
	priv := withTestKey(t)
	c := validClaims()
	c.Exp = time.Now().Add(-1 * time.Second).Unix()
	if _, err := Verify(sign(t, priv, c), "ABC123"); err == nil {
		t.Fatal("expired assertion accepted")
	}
}

func TestVerify_ForeignKeyRejected(t *testing.T) {
	withTestKey(t) // sets assertPub to key A
	// sign with a DIFFERENT key (key B) — a joiner forging their own assertion
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Verify(sign(t, otherPriv, validClaims()), "ABC123"); err == nil {
		t.Fatal("assertion signed by a non-partyline key accepted — forgery possible")
	}
}

func TestVerify_MalformedRejected(t *testing.T) {
	withTestKey(t)
	cases := map[string]string{
		"empty":        "",
		"no separator": "notanassertion",
		"bad b64 seg1": "!!!." + base64.RawURLEncoding.EncodeToString([]byte("x")),
		"bad b64 seg2": base64.RawURLEncoding.EncodeToString([]byte("{}")) + ".!!!",
		"not json":     base64.RawURLEncoding.EncodeToString([]byte("not json")) + "." + base64.RawURLEncoding.EncodeToString([]byte("x")),
	}
	for name, a := range cases {
		if _, err := Verify(a, "ABC123"); err == nil {
			t.Errorf("%s: malformed assertion accepted", name)
		}
	}
}

func TestLooksLikeAssertion(t *testing.T) {
	priv := withTestKey(t)
	if !LooksLikeAssertion(sign(t, priv, validClaims())) {
		t.Error("a real assertion should look like one")
	}
	if LooksLikeAssertion("alice") {
		t.Error("a plain name must not look like an assertion")
	}
}

// cut splits on the first "." (small local helper to avoid importing strings).
func cut(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
