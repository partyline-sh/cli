package remote

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// A command result can be far larger than one encrypted frame (loop details are allowed up
// to 512 KiB). The control lane must never build a frame over the Noise limit: such a frame
// is skipped by the batcher every window, so the peer stalls for good and its queue grows.
func TestOversizedControlResultStillArrives(t *testing.T) {
	p, _, receive := channelPeers(t)
	big := strings.Repeat("x", 200*1024)
	p.QueueControl(Message{Type: "loop_detail_result", ID: "big", Status: "ok", Data: big})
	p.QueueControl(Message{Type: "input_result", ID: "small", Status: "ok"})
	for i := 0; i < 400; i++ {
		p.QueueControl(Message{Type: "term_result", ID: "burst-" + strings.Repeat("0", 3) + string(rune('a'+i%26)), Status: "refused", Reason: strings.Repeat("r", 300)})
	}

	got := map[string]bool{}
	fragments := map[int][]string{}
	// As the relay batcher does: prepare queued snapshots, then size the next frame, then flush.
	next := func() int {
		if err := p.Prepare(); err != nil {
			t.Fatal(err)
		}
		return p.Size()
	}
	for i := 0; i < 200 && next() > 0; i++ {
		if n := p.Size(); n > MaxCiphertextBytes {
			t.Fatalf("frame %d would be %d bytes, over the %d-byte limit", i, n, MaxCiphertextBytes)
		}
		wire, err := p.Flush()
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) > MaxCiphertextBytes {
			t.Fatalf("flushed %d bytes, over the limit", len(wire))
		}
		plain, err := receive.Decrypt(nil, nil, wire)
		if err != nil {
			t.Fatal(err)
		}
		var msgs []map[string]any
		if err := json.Unmarshal(plain, &msgs); err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m["type"] == "fragment" {
				id := int(m["id"].(float64))
				fragments[id] = append(fragments[id], m["data"].(string))
				if len(fragments[id]) == int(m["parts"].(float64)) {
					var all []byte
					for _, part := range fragments[id] {
						b, _ := base64.StdEncoding.DecodeString(part)
						all = append(all, b...)
					}
					var inner []map[string]any
					if err := json.Unmarshal(all, &inner); err != nil {
						t.Fatal(err)
					}
					for _, m := range inner {
						got[m["id"].(string)] = true
					}
				}
				continue
			}
			if id, ok := m["id"].(string); ok {
				got[id] = true
			}
		}
	}
	if next() != 0 {
		t.Fatal("the control queue never drained")
	}
	var ids []string
	for id := range got {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if !got["big"] || !got["small"] {
		t.Fatalf("results delivered: %v", ids)
	}
}

// At the boundary every byte counts: brackets and the separating commas included. Two results
// sized so that the frame is exactly the limit, or one byte over it, must never yield a frame
// larger than the Noise limit.
func TestControlFrameExactBoundary(t *testing.T) {
	// Size each message with a one-byte reason, then subtract it: an empty reason is omitted
	// from the JSON entirely, which would throw the arithmetic off by the key's length.
	overhead := func(m Message) int { m.Reason = "x"; b, _ := json.Marshal(m); return len(b) - 1 }
	for extra := -2; extra <= 2; extra++ {
		p, _, _ := channelPeers(t)
		first := Message{Type: "r", ID: "a", Reason: ""}
		second := Message{Type: "r", ID: "b", Reason: ""}
		// [first,second] = 1 + len(first) + 1 + len(second) + 1 bytes.
		pad := controlFrameLimit - 3 - overhead(first) - overhead(second) + extra
		half := pad / 2
		first.Reason, second.Reason = strings.Repeat("x", half), strings.Repeat("y", pad-half)
		p.QueueControl(first, second)
		if err := p.Prepare(); err != nil {
			t.Fatal(err)
		}
		if n := p.Size(); n > MaxCiphertextBytes {
			t.Fatalf("extra=%d: frame would be %d bytes, over %d", extra, n, MaxCiphertextBytes)
		}
		wire, err := p.Flush()
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) > MaxCiphertextBytes {
			t.Fatalf("extra=%d: flushed %d bytes", extra, len(wire))
		}
	}
}
