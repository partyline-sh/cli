package wormhole

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello terminal bytes")
	if err := WriteFrame(&buf, FrameOutput, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	typ, got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != FrameOutput || !bytes.Equal(got, payload) {
		t.Errorf("round-trip mismatch: typ=%d got=%q", typ, got)
	}
}

func TestFrameEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameInput, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	typ, got, err := ReadFrame(&buf)
	if err != nil || typ != FrameInput || len(got) != 0 {
		t.Errorf("empty frame: typ=%d got=%q err=%v", typ, got, err)
	}
}

func TestWriteFrameRejectsOversized(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameOutput, make([]byte, maxFrame+1)); err == nil {
		t.Fatal("oversized payload should be rejected on write")
	}
}

// A malicious peer can claim any length in the header — ReadFrame must reject
// >maxFrame BEFORE allocating, or it's a memory-DoS (review M2/M3).
func TestReadFrameRejectsOversizedLength(t *testing.T) {
	var hdr [5]byte
	hdr[0] = byte(FrameOutput)
	binary.BigEndian.PutUint32(hdr[1:], maxFrame+1)
	if _, _, err := ReadFrame(bytes.NewReader(hdr[:])); err == nil {
		t.Fatal("over-cap length should be rejected before allocation")
	}
}

func TestReadFrameTruncated(t *testing.T) {
	var hdr [5]byte
	hdr[0] = byte(FrameOutput)
	binary.BigEndian.PutUint32(hdr[1:], 10)             // claims 10 bytes…
	r := bytes.NewReader(append(hdr[:], 'a', 'b', 'c')) // …but only 3 follow
	if _, _, err := ReadFrame(r); err == nil {
		t.Fatal("truncated payload should error")
	}
}

func TestResizeRoundTrip(t *testing.T) {
	cols, rows, ok := DecodeResize(EncodeResize(120, 40))
	if !ok || cols != 120 || rows != 40 {
		t.Errorf("resize round-trip: %d,%d ok=%v", cols, rows, ok)
	}
	if _, _, ok := DecodeResize([]byte{1, 2, 3}); ok {
		t.Error("short resize buffer should report ok=false")
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	ctrl := []byte(`{"code":"happy-otter-42-bcdf"}`)
	if err := WriteHeader(&buf, RoleJoiner, ctrl); err != nil {
		t.Fatalf("write header: %v", err)
	}
	role, got, err := ReadHeader(&buf)
	if err != nil || role != RoleJoiner || !bytes.Equal(got, ctrl) {
		t.Errorf("header round-trip: role=%c got=%q err=%v", role, got, err)
	}
}

func TestReadHeaderBadMagic(t *testing.T) {
	b := append([]byte("XXXX"), RoleJoiner, 0, 1, 'x')
	if _, _, err := ReadHeader(bytes.NewReader(b)); err == nil {
		t.Fatal("bad magic should be rejected")
	}
}

func TestReadHeaderRejectsBadCtrlLen(t *testing.T) {
	// zero-length ctrl is rejected
	zero := append([]byte(Magic), RoleHost, 0, 0)
	if _, _, err := ReadHeader(bytes.NewReader(zero)); err == nil {
		t.Error("zero ctrl length should be rejected")
	}
	// over-cap length (0xFFFF > maxCtrl) is rejected before allocation
	over := append([]byte(Magic), RoleHost, 0xFF, 0xFF)
	if _, _, err := ReadHeader(bytes.NewReader(over)); err == nil {
		t.Error("over-cap ctrl length should be rejected")
	}
}

func TestWriteHeaderRejectsOversizedCtrl(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHeader(&buf, RoleHost, make([]byte, maxCtrl+1)); err == nil {
		t.Fatal("oversized ctrl should be rejected")
	}
}

func TestAckRoundTrip(t *testing.T) {
	for _, want := range []bool{true, false} {
		var buf bytes.Buffer
		if err := WriteAck(&buf, want); err != nil {
			t.Fatalf("write ack: %v", err)
		}
		got, err := ReadAck(&buf)
		if err != nil || got != want {
			t.Errorf("ack round-trip: got=%v want=%v err=%v", got, want, err)
		}
	}
}
