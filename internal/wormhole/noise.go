// noise.go: a Noise NNpsk0 channel. The 32-byte link key is the pre-shared key.
// NNpsk0 gives us mutual authentication (a party that doesn't hold the link key —
// the relay — cannot complete the handshake, so it can neither read nor MITM) plus
// forward secrecy (fresh ephemeral DH per session). We never touch nonces or rekey:
// flynn/noise's CipherState owns that. We do NOT hand-roll crypto.
package wormhole

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/flynn/noise"
)

// KeyLen is the link-key / PSK length.
const KeyLen = 32

// maxNoiseMsg keeps each Noise message under the 65535-byte protocol limit; we
// chunk plaintext writes to leave room for the 16-byte AEAD tag.
const chunk = 16384

// Conn is an encrypted, framed-on-the-wire io.ReadWriteCloser over raw.
type Conn struct {
	raw  io.ReadWriteCloser
	send *noise.CipherState
	recv *noise.CipherState
	wmu  sync.Mutex
	rmu  sync.Mutex
	rrem []byte // leftover decrypted plaintext not yet returned by Read
}

// Initiator runs the NNpsk0 handshake as the initiator (the joiner).
func Initiator(raw io.ReadWriteCloser, psk []byte) (*Conn, error) {
	return handshake(raw, psk, true)
}

// Responder runs the NNpsk0 handshake as the responder (the host).
func Responder(raw io.ReadWriteCloser, psk []byte) (*Conn, error) {
	return handshake(raw, psk, false)
}

func handshake(raw io.ReadWriteCloser, psk []byte, initiator bool) (*Conn, error) {
	if len(psk) != KeyLen {
		return nil, fmt.Errorf("wormhole: psk must be %d bytes, got %d", KeyLen, len(psk))
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:           noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s),
		Pattern:               noise.HandshakeNN,
		Initiator:             initiator,
		PresharedKey:          psk,
		PresharedKeyPlacement: 0, // NNpsk0
	})
	if err != nil {
		return nil, err
	}

	// cs0 encrypts initiator→responder; cs1 encrypts responder→initiator.
	var cs0, cs1 *noise.CipherState
	if initiator {
		msg1, _, _, err := hs.WriteMessage(nil, nil)
		if err != nil {
			return nil, err
		}
		if err := writeChunk(raw, msg1); err != nil {
			return nil, err
		}
		msg2, err := readChunk(raw)
		if err != nil {
			return nil, err
		}
		if _, cs0, cs1, err = hs.ReadMessage(nil, msg2); err != nil {
			return nil, err // wrong PSK / tampered handshake → fails here
		}
		return &Conn{raw: raw, send: cs0, recv: cs1}, nil
	}

	msg1, err := readChunk(raw)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := hs.ReadMessage(nil, msg1); err != nil {
		return nil, err
	}
	msg2, cs0, cs1, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, err
	}
	if err := writeChunk(raw, msg2); err != nil {
		return nil, err
	}
	return &Conn{raw: raw, send: cs1, recv: cs0}, nil
}

func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > chunk {
			n = chunk
		}
		ct, err := c.send.Encrypt(nil, nil, p[:n])
		if err != nil {
			return total, err
		}
		if err := writeChunk(c.raw, ct); err != nil {
			return total, err
		}
		p = p[n:]
		total += n
	}
	return total, nil
}

func (c *Conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if len(c.rrem) == 0 {
		ct, err := readChunk(c.raw)
		if err != nil {
			return 0, err
		}
		pt, err := c.recv.Decrypt(nil, nil, ct)
		if err != nil {
			return 0, err // tampered/forged ciphertext → fails closed
		}
		c.rrem = pt
	}
	n := copy(p, c.rrem)
	c.rrem = c.rrem[n:]
	return n, nil
}

func (c *Conn) Close() error { return c.raw.Close() }

// length-prefixed (uint16) framing for each Noise message on the raw wire.
func writeChunk(w io.Writer, b []byte) error {
	if len(b) > 65535 {
		return fmt.Errorf("wormhole: noise message too large (%d)", len(b))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readChunk(r io.Reader) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(hdr[:]))
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
