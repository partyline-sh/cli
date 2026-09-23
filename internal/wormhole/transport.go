// transport.go: a Stream is a Noise Conn with serialized, typed frame I/O — the
// thing the host and joiner actually talk over. Frame writes are mutex-guarded so
// concurrent writers (e.g. the joiner's stdin loop + its SIGWINCH handler) can't
// interleave one frame inside another.
package wormhole

import (
	"io"
	"sync"
)

type Stream struct {
	c   *Conn
	wmu sync.Mutex
}

func NewStream(c *Conn) *Stream { return &Stream{c: c} }

func (s *Stream) WriteFrame(t FrameType, payload []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return WriteFrame(s.c, t, payload)
}

func (s *Stream) ReadFrame() (FrameType, []byte, error) { return ReadFrame(s.c) }

func (s *Stream) Close() error { return s.c.Close() }

// OutputWriter adapts a Stream to an io.Writer that emits FrameOutput — this is
// what we hand to ptysess.Attach as a participant's writer, so the engine stays
// transport-agnostic (it just writes terminal bytes; they go out encrypted).
func (s *Stream) OutputWriter() io.Writer { return outputWriter{s} }

type outputWriter struct{ s *Stream }

func (o outputWriter) Write(b []byte) (int, error) {
	if err := o.s.WriteFrame(FrameOutput, b); err != nil {
		return 0, err
	}
	return len(b), nil
}
