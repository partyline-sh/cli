package wormhole

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *lockedBuf) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf.Bytes()...)
}

// blindRelay wires joiner <-> relay <-> host with net.Pipe, copying bytes both
// ways WITHOUT decrypting, and teeing everything the relay forwards into `seen`.
// This is exactly the trust model: the relay is a dumb pipe.
func blindRelay(t *testing.T) (joiner, host net.Conn, seen *lockedBuf) {
	t.Helper()
	jConn, relayJ := net.Pipe()
	relayH, hConn := net.Pipe()
	seen = &lockedBuf{}
	go io.Copy(io.MultiWriter(relayH, seen), relayJ) // joiner→host
	go io.Copy(io.MultiWriter(relayJ, seen), relayH) // host→joiner
	return jConn, hConn, seen
}

func TestBlindRelayRoundTripAndConfidentiality(t *testing.T) {
	psk := bytes.Repeat([]byte{0x42}, KeyLen)
	jConn, hConn, seen := blindRelay(t)

	const secretIn = "rm -rf /tmp/very-secret-thing\n"
	const secretOut = "ALL THE SECRET TERMINAL OUTPUT\n"
	done := make(chan error, 1)

	// host (responder): read one Input frame, reply with one Output frame.
	go func() {
		c, err := Responder(hConn, psk)
		if err != nil {
			done <- err
			return
		}
		typ, payload, err := ReadFrame(c)
		if err != nil {
			done <- err
			return
		}
		if typ != FrameInput || string(payload) != secretIn {
			done <- &mismatch{"input", string(payload)}
			return
		}
		done <- WriteFrame(c, FrameOutput, []byte(secretOut))
	}()

	// joiner (initiator): send Input, read Output.
	c, err := Initiator(jConn, psk)
	if err != nil {
		t.Fatalf("initiator handshake: %v", err)
	}
	if err := WriteFrame(c, FrameInput, []byte(secretIn)); err != nil {
		t.Fatalf("write input: %v", err)
	}
	typ, payload, err := ReadFrame(c)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if typ != FrameOutput || string(payload) != secretOut {
		t.Fatalf("output mismatch: got %q", payload)
	}
	if err := <-done; err != nil {
		t.Fatalf("host side: %v", err)
	}

	// The relay must NOT have seen any plaintext.
	got := seen.Bytes()
	if bytes.Contains(got, []byte("secret")) || bytes.Contains(got, []byte("rm -rf")) ||
		bytes.Contains(got, []byte("TERMINAL")) {
		t.Fatalf("relay saw plaintext! confidentiality broken")
	}
	if len(got) == 0 {
		t.Fatal("relay saw nothing — test wiring is wrong")
	}
	t.Logf("relay forwarded %d bytes, all ciphertext ✓", len(got))
}

func TestWrongKeyFailsClosed(t *testing.T) {
	good := bytes.Repeat([]byte{0x42}, KeyLen)
	bad := bytes.Repeat([]byte{0x42}, KeyLen)
	bad[0] ^= 0x01 // flip one bit

	jConn, hConn, _ := blindRelay(t)

	// The host (responder) is where a wrong key is detected: NNpsk0 mixes the PSK
	// before the first message, so the host rejects a joiner (or a MITM relay)
	// that doesn't hold the link key. That's the property we care about.
	herr := make(chan error, 1)
	go func() { _, err := Responder(hConn, good); herr <- err }()
	go func() { _, _ = Initiator(jConn, bad) }() // wrong-key joiner; will get no reply

	select {
	case err := <-herr:
		if err == nil {
			t.Fatal("host accepted a wrong-key handshake — MITM/relay-blindness broken")
		}
		t.Logf("host rejected wrong key: %v ✓", err)
	case <-time.After(3 * time.Second):
		t.Fatal("host neither rejected nor completed the wrong-key handshake")
	}
}

type mismatch struct{ what, got string }

func (m *mismatch) Error() string { return "mismatch " + m.what + ": " + m.got }
