// Package remote implements the machine side of the mobile remote protocol.
package remote

import (
	"fmt"
	"io"

	"github.com/flynn/noise"
)

// NoiseConfig fixes the suite while allowing either pairing or a known-peer session.
// Random should be nil in production (crypto/rand); it exists for reproducible vectors.
type NoiseConfig struct {
	Pattern    string
	Initiator  bool
	Static     noise.DHKey
	PeerStatic []byte
	PSK        []byte
	Prologue   []byte
	Random     io.Reader
}

// NewNoise starts Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s or
// Noise_KK_25519_ChaChaPoly_BLAKE2s. The returned split ciphers are always
// initiator-to-responder first, responder-to-initiator second, on both peers.
// Handshake payloads and PeerStatic are provisional until the handshake completes.
// As with flynn/noise, states are ordered, single-owner, and not concurrency-safe.
// Callers must enforce the 65535-byte Noise wire limit when framing messages;
// flynn/noise does not bound transport ciphertext lengths.
func NewNoise(c NoiseConfig) (*noise.HandshakeState, error) {
	cfg := noise.Config{CipherSuite: noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s), Initiator: c.Initiator, StaticKeypair: c.Static, PeerStatic: c.PeerStatic, Prologue: c.Prologue, Random: c.Random}
	if len(c.Static.Private) != 32 || len(c.Static.Public) != 32 {
		return nil, fmt.Errorf("noise: static key must be 32 bytes")
	}
	if (c.Initiator || c.Pattern == "KK") && len(c.PeerStatic) != 32 {
		return nil, fmt.Errorf("noise: peer static must be 32 bytes")
	}
	switch c.Pattern {
	case "IKpsk2":
		if !c.Initiator && len(c.PeerStatic) != 0 {
			return nil, fmt.Errorf("noise: IK responder learns the peer static during the handshake")
		}
		if len(c.PSK) != 32 {
			return nil, fmt.Errorf("noise: PSK must be 32 bytes")
		}
		cfg.Pattern = noise.HandshakeIK
		cfg.PresharedKey = c.PSK
		cfg.PresharedKeyPlacement = 2
	case "KK":
		if len(c.PSK) != 0 {
			return nil, fmt.Errorf("noise: KK does not use a PSK")
		}
		cfg.Pattern = noise.HandshakeKK
	default:
		return nil, fmt.Errorf("noise: unsupported pattern %q", c.Pattern)
	}
	return noise.NewHandshakeState(cfg)
}
