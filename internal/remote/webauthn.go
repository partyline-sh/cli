package remote

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"

	"github.com/fxamacker/cbor/v2"
)

var webAuthnCBOR = func() cbor.DecMode {
	mode, err := (cbor.DecOptions{DupMapKey: cbor.DupMapKeyEnforcedAPF, MaxNestedLevels: 8, MaxArrayElements: 32, MaxMapPairs: 32, IndefLength: cbor.IndefLengthForbidden, TagsMd: cbor.TagsForbidden}).DecMode()
	if err != nil {
		panic(err)
	}
	return mode
}()

// WebAuthn intentionally accepts only ES256 credentials, the algorithm requested by
// the PWA. Credential IDs use unpadded base64url; []byte fields use Go JSON's
// standard base64 encoding, shared with the PWA wire encoder.
type Registration struct {
	ID          string `json:"id"`
	ClientData  []byte `json:"clientData"`
	Attestation []byte `json:"attestation"`
}
type Credential struct {
	ID        string `json:"id"`
	PublicKey []byte `json:"publicKey"` // COSE EC2 key
}
type Assertion struct {
	ID                string `json:"id"`
	ClientData        []byte `json:"clientData"`
	AuthenticatorData []byte `json:"authenticatorData"`
	Signature         []byte `json:"signature"`
}

func clientData(data, challenge []byte, origin, kind string) error {
	var c struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
		TopOrigin   string `json:"topOrigin"`
	}
	if len(challenge) != 32 || len(data) > 8192 || json.Unmarshal(data, &c) != nil || c.Type != kind || c.Origin != origin || c.CrossOrigin || c.TopOrigin != "" || c.Challenge != base64.RawURLEncoding.EncodeToString(challenge) {
		return errors.New("invalid WebAuthn client data")
	}
	return nil
}
func authData(data []byte, rpID string) error {
	hash := sha256.Sum256([]byte(rpID))
	if len(data) < 37 || !bytes.Equal(data[:32], hash[:]) || data[32]&5 != 5 || data[32]&0x22 != 0 || (data[32]&0x10 != 0 && data[32]&8 == 0) {
		return errors.New("invalid WebAuthn authenticator data")
	}
	return nil
}
func coseKey(data []byte) (*ecdsa.PublicKey, error) {
	var k struct {
		Kty int    `cbor:"1,keyasint"`
		Alg int    `cbor:"3,keyasint"`
		Crv int    `cbor:"-1,keyasint"`
		X   []byte `cbor:"-2,keyasint"`
		Y   []byte `cbor:"-3,keyasint"`
	}
	if webAuthnCBOR.Unmarshal(data, &k) != nil || k.Kty != 2 || k.Alg != -7 || k.Crv != 1 || len(k.X) != 32 || len(k.Y) != 32 {
		return nil, errors.New("unsupported WebAuthn public key")
	}
	key := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(k.X), Y: new(big.Int).SetBytes(k.Y)}
	if !key.Curve.IsOnCurve(key.X, key.Y) {
		return nil, errors.New("invalid WebAuthn point")
	}
	return key, nil
}

// ParseRegistration checks the creation ceremony and extracts its credential. None
// attestation is deliberate: possession is verified by a separate get assertion.
func ParseRegistration(r Registration, challenge []byte, origin, rpID string) (Credential, error) {
	var att struct {
		Format    string                     `cbor:"fmt"`
		AuthData  []byte                     `cbor:"authData"`
		Statement map[string]cbor.RawMessage `cbor:"attStmt"`
	}
	if err := clientData(r.ClientData, challenge, origin, "webauthn.create"); err != nil {
		return Credential{}, err
	}
	if len(r.Attestation) > 16384 || webAuthnCBOR.Unmarshal(r.Attestation, &att) != nil || att.Format != "none" || len(att.Statement) != 0 {
		return Credential{}, errors.New("invalid registration attestation")
	}
	a := att.AuthData
	if err := authData(a, rpID); err != nil {
		return Credential{}, err
	}
	if len(a) < 55 || a[32]&0xc0 != 0x40 {
		return Credential{}, errors.New("missing attested credential")
	}
	n := int(binary.BigEndian.Uint16(a[53:55]))
	if n == 0 || n > 1024 || len(a) <= 55+n || r.ID != base64.RawURLEncoding.EncodeToString(a[55:55+n]) {
		return Credential{}, errors.New("invalid credential id")
	}
	key := a[55+n:]
	if _, err := coseKey(key); err != nil {
		return Credential{}, err
	}
	return Credential{ID: r.ID, PublicKey: append([]byte(nil), key...)}, nil
}

// VerifyAssertion is shared by pairing and all later remote authorization checks.
// Callers own challenge freshness, expiry, and one-time consumption.
func VerifyAssertion(c Credential, a Assertion, challenge []byte, origin, rpID string) error {
	if a.ID != c.ID {
		return errors.New("wrong credential")
	}
	if err := clientData(a.ClientData, challenge, origin, "webauthn.get"); err != nil {
		return err
	}
	if err := authData(a.AuthenticatorData, rpID); err != nil {
		return err
	}
	// No extensions are requested. Reject attested data and trailing unsigned structure.
	if len(a.AuthenticatorData) != 37 || a.AuthenticatorData[32]&0xc0 != 0 {
		return errors.New("unexpected assertion data")
	}
	key, err := coseKey(c.PublicKey)
	if err != nil {
		return err
	}
	clientHash := sha256.Sum256(a.ClientData)
	signed := append(append([]byte(nil), a.AuthenticatorData...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	if !ecdsa.VerifyASN1(key, digest[:], a.Signature) {
		return errors.New("invalid WebAuthn signature")
	}
	return nil
}
