package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain isolates the whole package's tests from the developer's real ~/.partyline. The trust
// root is resolved from disk on every Verify, so a pin left by a real `ptln login` on this machine
// would otherwise change what the tests are testing — including the pre-existing ones, which must
// keep asserting the no-pin (compiled-in key) behaviour unchanged.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ptln-identity-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	os.Setenv("PARTYLINE_API", "") // production → ConfigDir is <home>/.partyline
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// noPin guarantees the test starts from the default (unpinned) state and leaves it that way.
func noPin(t *testing.T) {
	t.Helper()
	if err := ClearPin(); err != nil {
		t.Fatalf("clear pin: %v", err)
	}
	t.Cleanup(func() { _ = ClearPin() })
}

func genKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return pub, priv
}

func b64(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// pin writes a raw pin file, bypassing SavePin's validation, so the malformed cases are reachable.
func writeRawPin(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(dirOf(PinPath()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(PinPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("write pin: %v", err)
	}
	t.Cleanup(func() { _ = ClearPin() })
}

func dirOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "."
	}
	return p[:i]
}

// With NO pin, an assertion signed by the compiled-in default key verifies exactly as before.
// This is the guardrail: an existing install must behave identically.
func TestTrust_NoPin_DefaultKeyVerifies(t *testing.T) {
	noPin(t)
	priv := withTestKey(t) // withTestKey sets assertPub — i.e. the compiled-in default
	if _, err := Verify(sign(t, priv, validClaims()), "ABC123"); err != nil {
		t.Fatalf("no pin: default-key assertion must verify as today, got %v", err)
	}
}

// With a pin, an assertion signed by the PINNED key verifies — the self-host case.
func TestTrust_Pinned_PinnedKeyVerifies(t *testing.T) {
	noPin(t)
	withTestKey(t) // a default key that is NOT the pinned one
	pinPub, pinPriv := genKey(t)
	if err := SavePin("https://ptln.example.com", b64(pinPub)); err != nil {
		t.Fatalf("save pin: %v", err)
	}
	got, err := Verify(sign(t, pinPriv, validClaims()), "ABC123")
	if err != nil {
		t.Fatalf("pinned-key assertion must verify, got %v", err)
	}
	if got.Email != "alice@acme.com" {
		t.Errorf("claims not parsed: %+v", got)
	}
}

// ONE TRUST ROOT AT A TIME: with a pin in place, the compiled-in DEFAULT key must be rejected.
// A fallback to the default would make the trust root a set of two — and an attacker who cannot
// produce the self-hosted instance's signature would simply sign with the default one instead.
func TestTrust_Pinned_DefaultKeyRejected(t *testing.T) {
	noPin(t)
	defaultPriv := withTestKey(t)
	pinPub, _ := genKey(t)
	if err := SavePin("https://ptln.example.com", b64(pinPub)); err != nil {
		t.Fatalf("save pin: %v", err)
	}
	if _, err := Verify(sign(t, defaultPriv, validClaims()), "ABC123"); err == nil {
		t.Fatal("a pin is in force but an assertion signed by the compiled-in DEFAULT key was accepted — the trust root is a set, not a pin")
	}
}

// A malformed or wrong-length pin REFUSES rather than falling back to accepting anything. Falling
// back to the default here would mean corrupting one small file silently restores the old trust
// root — an attacker with any write access to it could downgrade the pin away.
func TestTrust_BrokenPin_RefusesEverything(t *testing.T) {
	shortKey := base64.StdEncoding.EncodeToString([]byte("too-short"))
	longKey := base64.StdEncoding.EncodeToString(make([]byte, 64))
	cases := map[string]string{
		"not json":     `{not json`,
		"empty key":    `{"base":"https://x","key":""}`,
		"not base64":   `{"base":"https://x","key":"!!!not base64!!!"}`,
		"short key":    `{"base":"https://x","key":"` + shortKey + `"}`,
		"long key":     `{"base":"https://x","key":"` + longKey + `"}`,
		"empty object": `{}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			noPin(t)
			defaultPriv := withTestKey(t)
			writeRawPin(t, body)
			// Signed by the default key: the only way this could pass is a fallback.
			if _, err := Verify(sign(t, defaultPriv, validClaims()), "ABC123"); err == nil {
				t.Fatal("a broken pin fell back to accepting the default key")
			}
			// And by a fresh key, i.e. "accept anything".
			_, otherPriv := genKey(t)
			if _, err := Verify(sign(t, otherPriv, validClaims()), "ABC123"); err == nil {
				t.Fatal("a broken pin accepted an arbitrary key")
			}
		})
	}
}

// A CHANGED key REFUSES, and the refusal is actionable: it names BOTH fingerprints and the exact
// command that accepts the new one. Warn-and-continue is not an option — there is no code path
// through CheckOfferedKey that returns nil on a mismatch.
func TestTrust_ChangedKeyRefusesWithBothFingerprints(t *testing.T) {
	noPin(t)
	pinnedPub, _ := genKey(t)
	offeredPub, _ := genKey(t)
	if err := SavePin("https://ptln.example.com", b64(pinnedPub)); err != nil {
		t.Fatalf("save pin: %v", err)
	}
	err := CheckOfferedKey(b64(offeredPub))
	if err == nil {
		t.Fatal("a changed identity key was accepted — the pin detects nothing")
	}
	msg := err.Error()
	for _, want := range []string{
		Fingerprint(pinnedPub),              // what we trusted
		Fingerprint(offeredPub),             // what we were just handed
		"ptln login <url> --accept-new-key", // how to accept it after verifying on the server
		"REFUSED",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message missing %q:\n%s", want, msg)
		}
	}
	if Fingerprint(pinnedPub) == Fingerprint(offeredPub) {
		t.Fatal("test bug: the two fingerprints are identical")
	}
}

// The pinned key itself is accepted, and with nothing pinned the first key offered is accepted
// (that is the TOFU step — the caller then pins it).
func TestTrust_CheckOfferedKey_SameAndUnpinned(t *testing.T) {
	noPin(t)
	pub, _ := genKey(t)
	if err := CheckOfferedKey(b64(pub)); err != nil {
		t.Fatalf("unpinned: first key offered must be accepted (TOFU), got %v", err)
	}
	if err := SavePin("https://ptln.example.com", b64(pub)); err != nil {
		t.Fatalf("save pin: %v", err)
	}
	if err := CheckOfferedKey(b64(pub)); err != nil {
		t.Fatalf("the pinned key must be accepted, got %v", err)
	}
}

// An unusable offered key is refused rather than pinned — decodeKey also guards ed25519.Verify,
// which PANICS on a wrong-size key (a panic in the verifier is a DoS on the host's terminal).
func TestTrust_CheckOfferedKey_Unusable(t *testing.T) {
	noPin(t)
	for _, bad := range []string{"", "!!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if err := CheckOfferedKey(bad); err == nil {
			t.Errorf("unusable offered key %q accepted", bad)
		}
	}
}

// Re-pinning REPLACES; it never accumulates. After pointing at a second instance, only the second
// instance's assertions verify.
func TestTrust_RepinReplaces(t *testing.T) {
	noPin(t)
	withTestKey(t)
	firstPub, firstPriv := genKey(t)
	secondPub, secondPriv := genKey(t)
	if err := SavePin("https://a.example.com", b64(firstPub)); err != nil {
		t.Fatal(err)
	}
	if err := SavePin("https://b.example.com", b64(secondPub)); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(sign(t, secondPriv, validClaims()), "ABC123"); err != nil {
		t.Fatalf("the newly pinned instance must verify, got %v", err)
	}
	if _, err := Verify(sign(t, firstPriv, validClaims()), "ABC123"); err == nil {
		t.Fatal("the previously pinned key still verifies — pinning accumulated instead of replacing")
	}
	p, ok := LoadPin()
	if !ok || p.Base != "https://b.example.com" {
		t.Errorf("pin base = %q (ok=%v), want the instance most recently pinned", p.Base, ok)
	}
}

// SavePin refuses to write a key it could not verify with, so a bad fetch cannot brick the client.
func TestTrust_SavePinRejectsBadKey(t *testing.T) {
	noPin(t)
	if err := SavePin("https://x", "not-base64!!!"); err == nil {
		t.Fatal("SavePin accepted a non-base64 key")
	}
	if _, ok := LoadPin(); ok {
		t.Fatal("a rejected key still wrote a pin file")
	}
}

// TrustRoot is what the CLI prints; it must say which root is in force, not just a fingerprint.
func TestTrust_TrustRootReporting(t *testing.T) {
	noPin(t)
	withTestKey(t)
	fp, source, base, err := TrustRoot()
	if err != nil || fp != Fingerprint(DefaultKey()) || !strings.Contains(source, "built-in") || base != "" {
		t.Fatalf("unpinned: got (%q,%q,%q,%v)", fp, source, base, err)
	}
	pub, _ := genKey(t)
	if err := SavePin("https://ptln.example.com", b64(pub)); err != nil {
		t.Fatal(err)
	}
	fp, source, base, err = TrustRoot()
	if err != nil || fp != Fingerprint(pub) || source != "pinned" || base != "https://ptln.example.com" {
		t.Fatalf("pinned: got (%q,%q,%q,%v)", fp, source, base, err)
	}
}

// Fingerprint is the ssh-keygen shape (SHA256:<unpadded base64>) so a human can compare the two
// ends by eye, and is stable for a given key.
func TestTrust_FingerprintShape(t *testing.T) {
	pub, _ := genKey(t)
	fp := Fingerprint(pub)
	if !strings.HasPrefix(fp, "SHA256:") || strings.Contains(fp, "=") || len(fp) != len("SHA256:")+43 {
		t.Fatalf("unexpected fingerprint shape: %q", fp)
	}
	other, err := FingerprintB64(b64(pub))
	if err != nil || other != fp {
		t.Fatalf("FingerprintB64 = (%q,%v), want %q", other, err, fp)
	}
}

// PublicFromPrivateB64 is how `ptln server doctor` prints the fingerprint clients should have
// pinned: same shape the control plane signs with (base64 of a PKCS#8 PEM), public half only.
func TestTrust_PublicFromPrivateB64(t *testing.T) {
	pub, priv := genKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	got, err := PublicFromPrivateB64(base64.StdEncoding.EncodeToString(pemBytes))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if Fingerprint(got) != Fingerprint(pub) {
		t.Fatal("derived the wrong public key")
	}
	// A garbage value must error WITHOUT quoting what it contained (doctor output is pasteable).
	const secret = "SUPERSECRET-VALUE"
	_, err = PublicFromPrivateB64(secret)
	if err == nil {
		t.Fatal("garbage accepted as a private key")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error leaked the value: %v", err)
	}
}
