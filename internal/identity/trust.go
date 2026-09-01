package identity

// The trust root: WHICH Ed25519 public key this CLI accepts identity assertions from.
//
// The control plane signs a short-lived, session-scoped assertion ("partyline says this is
// alice@acme.com, joining this code") and the HOST of a shared terminal verifies it here. So the
// trust root is how a host knows who is joining: get it wrong and someone joins your terminal as a
// person they are not. Four decisions (docs/epics/self-host.md H.2) constrain everything below —
// none of them is a preference:
//
//  1. ONE TRUST ROOT AT A TIME. Exactly one key is consulted; pointing the CLI at another instance
//     REPLACES it. Never a set, never a try-all loop — with several trusted keys, ANY instance you
//     ever trusted could forge an identity in ANY session, so trusting a client's self-hosted box
//     once would let it impersonate you on partyline.sh. This also keeps the assertion format
//     unchanged (no issuer field), which keeps a compatibility problem out of a security change.
//  2. TOFU OVER HTTPS. `ptln login <url>` fetches the instance's key over TLS, pins it, and prints
//     the fingerprint. Not SSH's bare TOFU: TLS already authenticates the host, so an attacker
//     needs a valid certificate for that domain. The fingerprint is printed, not enforced —
//     forcing a confirmation people paste through buys the appearance of security, not security.
//  3. A CHANGED KEY REFUSES. Warn-and-continue was considered and rejected: the pin exists to
//     DETECT key substitution, so if detection stops nothing the pin provides no security at all.
//     The refusal names BOTH fingerprints and the one command that accepts the new key.
//  4. NO AUTO-ROTATION. A new key is never signed by the old one. That fails in exactly the case
//     anyone rotates for — if the OLD key is what leaked, its holder mints a successor every client
//     silently adopts. Re-pinning is manual, one command per client.
//
// Default behaviour is unchanged: with NO pin, verification uses the compiled-in partyline.sh key
// exactly as before, so an existing install behaves identically.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"partyline.sh/partyline/internal/api"
)

// Pin is the pinned trust root: the one instance whose assertions this CLI accepts.
// Stored beside the device credential (~/.partyline/daemon/), which is already the home for
// per-control-plane machine state and is already 0700. api.ConfigDir() is per-endpoint, so a
// staging login cannot silently re-point the trust root used against production.
type Pin struct {
	Base     string `json:"base"`      // the instance URL this key was fetched from
	Key      string `json:"key"`       // raw 32-byte Ed25519 public key, base64-std
	PinnedAt string `json:"pinned_at"` // RFC3339, for "when did this machine start trusting it"
}

// PinPath is where the pinned trust root lives for the control plane this binary is pointed at.
func PinPath() string { return filepath.Join(api.ConfigDir(), "daemon", "trust.json") }

// LoadPin returns the pinned trust root, and whether a pin file is present at all. A present but
// unparseable file still reports ok=true with an empty Key: "there is a pin and it is broken" must
// never be mistaken for "there is no pin", because the second one falls back to the default key.
func LoadPin() (Pin, bool) {
	b, err := os.ReadFile(PinPath())
	if err != nil {
		return Pin{}, false
	}
	var p Pin
	if err := json.Unmarshal(b, &p); err != nil {
		return Pin{}, true
	}
	return p, true
}

// SavePin replaces the trust root. Replaces — there is deliberately no "add".
func SavePin(base, keyB64 string) error {
	if _, err := decodeKey(keyB64); err != nil {
		return err
	}
	p := Pin{Base: strings.TrimRight(strings.TrimSpace(base), "/"), Key: strings.TrimSpace(keyB64),
		PinnedAt: time.Now().UTC().Format(time.RFC3339)}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(PinPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(PinPath(), append(b, '\n'), 0o600)
}

// ClearPin drops the pin, returning this CLI to the compiled-in default key. A no-op if absent.
func ClearPin() error {
	if err := os.Remove(PinPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// decodeKey parses a raw Ed25519 public key in base64-std. Anything of the wrong length is an
// error rather than a truncated/padded key: ed25519.Verify PANICS on a wrong-size key, and a
// verifier that panics on a hostile input is a denial of service on the host's terminal.
func decodeKey(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("not valid base64")
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("wrong length: %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// Fingerprint is the ssh-keygen-shaped fingerprint of a public key: SHA256:<base64, unpadded>.
// Safe to print anywhere — it is a one-way hash of a PUBLIC key.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// FingerprintB64 fingerprints a base64-std raw public key.
func FingerprintB64(b64 string) (string, error) {
	pub, err := decodeKey(b64)
	if err != nil {
		return "", err
	}
	return Fingerprint(pub), nil
}

// DefaultKey is the compiled-in partyline.sh assertion key — the trust root when nothing is pinned.
func DefaultKey() ed25519.PublicKey { return assertPub }

const acceptCmd = "ptln login <url> --accept-new-key"

// trustRoot resolves the ONE key assertions are verified against.
//
// A pin, when present, is the whole answer: there is deliberately no fallback to the compiled-in
// key, because a fallback IS a two-key trust set — an attacker who cannot produce the pinned
// instance's signature would simply sign with the default key instead and be accepted.
// A pin that cannot be decoded therefore refuses everything rather than degrading to the default.
func trustRoot() (ed25519.PublicKey, error) {
	p, ok := LoadPin()
	if !ok {
		return assertPub, nil
	}
	pub, err := decodeKey(p.Key)
	if err != nil {
		return nil, fmt.Errorf("pinned trust root at %s is unusable (%v) — verification refused.\n"+
			"  Fix it by re-pinning against the instance after checking its fingerprint with `ptln server doctor` there:\n"+
			"    %s", PinPath(), err, acceptCmd)
	}
	return pub, nil
}

// TrustRoot reports the active trust root for display: its fingerprint, where it came from, and
// the instance it was pinned from (empty when the compiled-in default is in force).
func TrustRoot() (fingerprint, source, base string, err error) {
	p, ok := LoadPin()
	pub, err := trustRoot()
	if err != nil {
		return "", "pinned (unusable)", p.Base, err
	}
	if !ok {
		return Fingerprint(pub), "built-in (partyline.sh)", "", nil
	}
	return Fingerprint(pub), "pinned", p.Base, nil
}

// CheckOfferedKey is the changed-key gate. It is called wherever an instance OFFERS its assertion
// key — today that is `ptln login`, over TLS. With no pin it accepts (that is the TOFU step, and
// the caller pins what it was handed). With a pin it accepts only the pinned key, byte for byte,
// and otherwise REFUSES with both fingerprints and the exact command to accept the new one.
//
// There is deliberately no "continue anyway" path in here: the caller either gets nil or stops.
func CheckOfferedKey(offeredB64 string) error {
	offered, err := decodeKey(offeredB64)
	if err != nil {
		return fmt.Errorf("this instance offered an unusable assertion key (%v) — refusing", err)
	}
	p, ok := LoadPin()
	if !ok {
		return nil // nothing pinned yet: trust on first use
	}
	pinned, err := decodeKey(p.Key)
	if err != nil {
		return fmt.Errorf("the pinned trust root at %s is unusable (%v) — refusing.\n"+
			"  Offered key: %s\n"+
			"  Accept it only after confirming that fingerprint on the server (`ptln server doctor` there):\n"+
			"    %s", PinPath(), err, Fingerprint(offered), acceptCmd)
	}
	if subtle.ConstantTimeCompare(pinned, offered) == 1 {
		return nil
	}
	return fmt.Errorf("REFUSED: this instance's identity key changed.\n"+
		"  pinned   %s  (from %s)\n"+
		"  offered  %s\n"+
		"  A changed key means either a legitimate rotation or someone impersonating this instance —\n"+
		"  and the host of a shared terminal trusts it to say who is joining, so partyline stops here.\n"+
		"  Verify the offered fingerprint ON THE SERVER (`ptln server doctor` there) and, only if it matches, run:\n"+
		"    %s", Fingerprint(pinned), pinBaseOrUnknown(p), Fingerprint(offered), acceptCmd)
}

func pinBaseOrUnknown(p Pin) string {
	if strings.TrimSpace(p.Base) == "" {
		return "an unrecorded instance"
	}
	return p.Base
}

// PublicFromPrivateB64 derives the assertion PUBLIC key from the value an instance holds in
// PARTYLINE_ASSERT_KEY (base64 of a PKCS#8 PEM Ed25519 private key — the shape the control plane's
// signer expects, see web/src/app/api/v1/identity/route.ts). It exists so `ptln server doctor`,
// which runs ON the box, can print the fingerprint a client should have pinned WITHOUT the box
// having to be reachable or the doctor having to talk to itself.
//
// The returned error never quotes the input: this is the one place the doctor touches a secret's
// value, and doctor output is meant to be pasteable into an issue.
func PublicFromPrivateB64(b64 string) (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("not base64")
	}
	block, _ := pem.Decode(der)
	if block == nil {
		return nil, fmt.Errorf("not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a PKCS#8 private key")
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an Ed25519 key")
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an Ed25519 key")
	}
	return pub, nil
}
