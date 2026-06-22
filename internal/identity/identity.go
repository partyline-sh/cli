// Package identity: partyline-native join identity. The control plane signs a
// short-lived, session-scoped assertion ("partyline says this is alice@acme.com,
// joining this code") with its Ed25519 key; the host verifies it here with the
// baked-in public key. No GitHub, no dependency on the joiner's auth method —
// partyline's own auth (email / Google / GitHub-OAuth) is the source of truth.
package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// assertPubKey is partyline's assertion public key (raw 32 bytes, base64-std).
// The control plane holds the matching private key (CPLN secret
// prod-partyline-assert-key). Rotation = new keypair + update this const + rebuild.
const assertPubKeyB64 = "i00mYQ4MDb71Aax0V9RnARYjpZRO/IuQ2aZdaBKQUgI="

var assertPub ed25519.PublicKey

func init() {
	b, err := base64.StdEncoding.DecodeString(assertPubKeyB64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("identity: bad baked assertion pubkey")
	}
	assertPub = ed25519.PublicKey(b)
}

type Claims struct {
	Sub    string `json:"sub"`    // partyline handle
	Name   string `json:"name"`   // display name
	Email  string `json:"email"`  // verified email
	Code   string `json:"code"`   // the session this assertion is scoped to
	Access string `json:"access"` // 'viewer' | 'full' — host gates driving on this (#3)
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
}

// Verify checks a `<b64url(jsonClaims)>.<b64url(sig)>` assertion: the signature is
// valid under partyline's key, the code matches this session, and it hasn't
// expired. Fails closed. The signature covers the first segment's ASCII bytes
// (the transmitted base64url), so there's no JSON-canonicalization ambiguity.
func Verify(assertion, code string) (*Claims, error) {
	seg1, seg2, ok := strings.Cut(assertion, ".")
	if !ok {
		return nil, fmt.Errorf("malformed assertion")
	}
	payload, err := base64.RawURLEncoding.DecodeString(seg1)
	if err != nil {
		return nil, fmt.Errorf("bad payload encoding")
	}
	sig, err := base64.RawURLEncoding.DecodeString(seg2)
	if err != nil {
		return nil, fmt.Errorf("bad sig encoding")
	}
	if !ed25519.Verify(assertPub, []byte(seg1), sig) {
		return nil, fmt.Errorf("bad signature")
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("bad claims")
	}
	if c.Code != code {
		return nil, fmt.Errorf("assertion not for this session")
	}
	if time.Now().Unix() > c.Exp {
		return nil, fmt.Errorf("assertion expired")
	}
	return &c, nil
}

// LooksLikeAssertion distinguishes a signed assertion from a plain self-asserted
// name, so the host knows whether to verify or just sanitize.
func LooksLikeAssertion(s string) bool {
	return strings.Contains(s, ".") && len(s) > 80
}
