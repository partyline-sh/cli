package bus

import (
	"encoding/json"
	"testing"
	"time"
)

func conn(id, org string, projects ...string) *Conn {
	return &Conn{Identity: id, Org: org, Projects: projects, Out: make(chan Envelope, 4)}
}

// The one mistake a shared bus must not make: a message reaching another customer. Org is
// checked before anything else, whatever the envelope claims.
func TestPublishNeverCrossesOrgs(t *testing.T) {
	h := NewHub()
	mine, theirs := conn("darcy@mac", "org-a", "acr"), conn("stranger@box", "org-b", "acr")
	h.Add(mine)
	h.Add(theirs)
	n := h.Publish("org-a", Envelope{Type: TypeMemoryChanged, Project: "acr", From: "matt@linux"})
	if n != 1 {
		t.Fatalf("delivered to %d connections, want 1 (only the sender's org)", n)
	}
	select {
	case <-theirs.Out:
		t.Fatal("a message crossed into another org")
	default:
	}
}

// Delivery is scoped to the project, and a sender never receives its own message.
func TestPublishScopesAndDoesNotEcho(t *testing.T) {
	h := NewHub()
	same, other, sender := conn("a", "o", "acr"), conn("b", "o", "hoops"), conn("matt@linux", "o", "acr")
	for _, c := range []*Conn{same, other, sender} {
		h.Add(c)
	}
	if n := h.Publish("o", Envelope{Type: TypeAsk, Project: "acr", From: "matt@linux"}); n != 1 {
		t.Fatalf("delivered to %d, want 1 (project subscriber, not the other project, not the sender)", n)
	}
	select {
	case <-other.Out:
		t.Fatal("a message reached a connection that never subscribed to that project")
	default:
	}
	select {
	case <-sender.Out:
		t.Fatal("the sender received its own message")
	default:
	}
}

// An addressed message goes to one peer only — this is what makes agent-to-agent asks possible
// on a shared channel.
func TestAddressedMessage(t *testing.T) {
	h := NewHub()
	target, bystander := conn("darcy@mac", "o", "acr"), conn("someone@else", "o", "acr")
	h.Add(target)
	h.Add(bystander)
	h.Publish("o", Envelope{Type: TypeAsk, Project: "acr", From: "matt@linux", To: "darcy@mac"})
	if len(target.Out) != 1 {
		t.Error("the addressee did not receive it")
	}
	if len(bystander.Out) != 0 {
		t.Error("an addressed message was broadcast")
	}
}

// One stalled client must never freeze the fan-out for everyone else. A full buffer drops; the
// receiver re-syncs from git, which is why the bus may lose a message and the store may not.
func TestSlowClientIsDroppedNotBlocking(t *testing.T) {
	h := NewHub()
	slow, healthy := conn("slow", "o", "acr"), conn("healthy", "o", "acr")
	h.Add(slow)
	h.Add(healthy)
	for i := 0; i < cap(slow.Out); i++ {
		slow.Out <- Envelope{}
	}
	done := make(chan int, 1)
	go func() { done <- h.Publish("o", Envelope{Type: TypeMemoryChanged, Project: "acr"}) }()
	select {
	case n := <-done:
		if n != 1 {
			t.Errorf("healthy client should still have received it, delivered=%d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a client that is not reading")
	}
}

// A reconnect must not leave a zombie taking half the traffic.
func TestReconnectReplacesTheOldConnection(t *testing.T) {
	h := NewHub()
	first := conn("darcy@mac", "o", "acr")
	h.Add(first)
	h.Add(conn("darcy@mac", "o", "acr"))
	if h.Count() != 1 {
		t.Fatalf("hub holds %d connections for one identity", h.Count())
	}
	if _, open := <-first.Out; open {
		t.Error("the replaced connection was left open")
	}
}

// The envelope is the contract: unknown types and unscoped messages are refused, and the bus
// refuses to carry content rather than news.
func TestValidate(t *testing.T) {
	if err := (Envelope{Type: "whatever", Project: "acr"}).Validate(); err == nil {
		t.Error("an unknown type was accepted")
	}
	if err := (Envelope{Type: TypeAsk}).Validate(); err == nil {
		t.Error("a message with no project was accepted")
	}
	big, _ := json.Marshal(map[string]string{"transcript": string(make([]byte, maxPayload+1))})
	if err := (Envelope{Type: TypeAsk, Project: "acr", Payload: big}).Validate(); err == nil {
		t.Error("an oversized payload was accepted — the bus carries news, not content")
	}
	if err := (Envelope{Type: TypeMemoryChanged, Project: "acr"}).Validate(); err != nil {
		t.Errorf("a valid message was refused: %v", err)
	}
}
