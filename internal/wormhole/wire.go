// wire.go: the tiny CLEARTEXT rendezvous protocol between a client and the relay,
// spoken before the Noise handshake. The relay reads only this — a magic, a role,
// and a small JSON control blob (host: {code,token}; joiner: {code}). Everything
// after is opaque ciphertext the relay splices blind. Shared by the relay binary
// and the host/joiner clients so there's one source of truth for the format.
package wormhole

import (
	"encoding/binary"
	"fmt"
	"io"
)

const Magic = "PLR1"

const (
	RoleHost   byte = 'H'
	RoleJoiner byte = 'J'
)

const maxCtrl = 4096

// HostCtrl / JoinCtrl are the JSON control payloads.
type HostCtrl struct {
	Code  string `json:"code"`
	Token string `json:"token"`
}
type JoinCtrl struct {
	Code string `json:"code"`
}

// WriteHeader sends magic + role + length-prefixed ctrl.
func WriteHeader(w io.Writer, role byte, ctrl []byte) error {
	if len(ctrl) > maxCtrl {
		return fmt.Errorf("wormhole: ctrl too large")
	}
	buf := make([]byte, 0, 7+len(ctrl))
	buf = append(buf, Magic...)
	buf = append(buf, role)
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(ctrl)))
	buf = append(buf, lb[:]...)
	buf = append(buf, ctrl...)
	_, err := w.Write(buf)
	return err
}

// ReadHeader reads magic + role + ctrl. Caller should set a read deadline first.
func ReadHeader(r io.Reader) (role byte, ctrl []byte, err error) {
	h := make([]byte, 5)
	if _, err = io.ReadFull(r, h); err != nil {
		return 0, nil, err
	}
	if string(h[:4]) != Magic {
		return 0, nil, fmt.Errorf("wormhole: bad magic")
	}
	role = h[4]
	var lb [2]byte
	if _, err = io.ReadFull(r, lb[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint16(lb[:])
	if n == 0 || int(n) > maxCtrl {
		return 0, nil, fmt.Errorf("wormhole: bad ctrl length %d", n)
	}
	ctrl = make([]byte, n)
	if _, err = io.ReadFull(r, ctrl); err != nil {
		return 0, nil, err
	}
	return role, ctrl, nil
}

// WriteAck / ReadAck: a single byte (1 = ok) the relay sends after routing.
func WriteAck(w io.Writer, ok bool) error {
	b := byte(0)
	if ok {
		b = 1
	}
	_, err := w.Write([]byte{b})
	return err
}

func ReadAck(r io.Reader) (bool, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return false, err
	}
	return b[0] == 1, nil
}
