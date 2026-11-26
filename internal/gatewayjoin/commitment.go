package gatewayjoin

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
)

// Message 1 commits to the initiator's static key, random nonce and exact request
// bytes before the responder reveals its ephemeral key. Message 3 reveals them.
// Committing to the raw bytes also binds whitespace and unknown JSON fields: no
// final-message input remains available for offline comparison-code grinding.
// The 256-bit nonce hides the reveal until the initiator sends message 3, so a
// fake responder cannot predict the code while choosing its message 2 either.
func joinCommitment(static, nonce, payload []byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte("ptln private gateway join commitment v2\x00"))
	h.Write(static) // 32 bytes
	h.Write(nonce)  // 32 bytes
	h.Write(payload)
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

func revealJoin(commitment, static, revealed []byte) ([]byte, error) {
	if len(commitment) != sha256.Size || len(static) != 32 || len(revealed) < sha256.Size {
		return nil, errors.New("invalid join commitment reveal")
	}
	nonce, payload := revealed[:sha256.Size], revealed[sha256.Size:]
	expected := joinCommitment(static, nonce, payload)
	if subtle.ConstantTimeCompare(expected[:], commitment) != 1 {
		return nil, errors.New("join commitment does not match")
	}
	return payload, nil
}
