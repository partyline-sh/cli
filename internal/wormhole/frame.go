// Package wormhole: the end-to-end-encrypted transport between a partyline host
// and a joiner. The relay only ever sees the ciphertext produced here — it has
// no key and cannot read or modify the session. See docs (E2EE) + the plan.
//
// frame.go: the typed messages that ride INSIDE the encrypted channel. The host
// glue maps these onto ptysess (output bytes out, input/resize in); ptysess
// itself is unchanged and transport-agnostic.
package wormhole

import (
	"encoding/binary"
	"fmt"
	"io"
)

type FrameType byte

const (
	FrameOutput   FrameType = 1 // host→joiner: raw terminal bytes (incl. the join snapshot)
	FrameInput    FrameType = 2 // joiner→host: raw keystrokes / prefix + /p commands
	FrameResize   FrameType = 3 // joiner→host: [cols u32][rows u32]
	FrameIdentity FrameType = 4 // joiner→host: name (+ future GitHub-key proof)
	FrameClose    FrameType = 5 // host→joiner: session ended gracefully — stop, don't reconnect
	FrameRepaint  FrameType = 6 // joiner→host: resend a full-screen snapshot (after a local overlay)
)

const maxFrame = 1 << 20 // 1 MiB hard cap — defends against a malformed length

// WriteFrame writes [type:1][len:u32-be][payload] to w (which is the encrypted
// wormhole.Conn — so this lands as ciphertext on the wire).
func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	if len(payload) > maxFrame {
		return fmt.Errorf("wormhole: frame too large (%d)", len(payload))
	}
	var hdr [5]byte
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// ReadFrame reads one typed frame.
func ReadFrame(r io.Reader) (FrameType, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("wormhole: frame length %d exceeds cap", n)
	}
	buf := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, nil, err
		}
	}
	return FrameType(hdr[0]), buf, nil
}

// EncodeResize / DecodeResize for the FrameResize payload.
func EncodeResize(cols, rows int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:4], uint32(cols))
	binary.BigEndian.PutUint32(b[4:8], uint32(rows))
	return b
}

func DecodeResize(b []byte) (cols, rows int, ok bool) {
	if len(b) < 8 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint32(b[0:4])), int(binary.BigEndian.Uint32(b[4:8])), true
}
