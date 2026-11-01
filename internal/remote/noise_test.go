package remote

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/flynn/noise"
)

type noiseMessage struct {
	Sender     string `json:"sender"`
	Payload    string `json:"payload"`
	Ciphertext string `json:"ciphertext"`
}
type noiseVector struct {
	Pattern            string         `json:"pattern"`
	Prologue           string         `json:"prologue"`
	InitiatorStatic    string         `json:"initiatorStatic"`
	ResponderStatic    string         `json:"responderStatic"`
	InitiatorEphemeral string         `json:"initiatorEphemeral"`
	ResponderEphemeral string         `json:"responderEphemeral"`
	InitiatorPublic    string         `json:"initiatorPublic"`
	ResponderPublic    string         `json:"responderPublic"`
	PSK                string         `json:"psk,omitempty"`
	Handshake          []noiseMessage `json:"handshake"`
	Hash               string         `json:"hash"`
	Transport          []noiseMessage `json:"transport"`
}

func noiseKey(t *testing.T, seed byte) noise.DHKey {
	t.Helper()
	k, err := noise.DH25519.GenerateKeypair(bytes.NewReader(bytes.Repeat([]byte{seed}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func noisePeers(t *testing.T, pattern string, wrongPSK bool, prologue []byte) (*noise.HandshakeState, *noise.HandshakeState, noiseVector) {
	t.Helper()
	a, b := noiseKey(t, 1), noiseKey(t, 2)
	psk := bytes.Repeat([]byte{5}, 32)
	v := noiseVector{Pattern: pattern, Prologue: hex.EncodeToString(prologue), InitiatorStatic: hex.EncodeToString(a.Private), ResponderStatic: hex.EncodeToString(b.Private), InitiatorPublic: hex.EncodeToString(a.Public), ResponderPublic: hex.EncodeToString(b.Public), InitiatorEphemeral: hex.EncodeToString(bytes.Repeat([]byte{3}, 32)), ResponderEphemeral: hex.EncodeToString(bytes.Repeat([]byte{4}, 32))}
	if pattern == "IKpsk2" {
		v.PSK = hex.EncodeToString(psk)
	} else {
		psk = nil
	}
	i, err := NewNoise(NoiseConfig{Pattern: pattern, Initiator: true, Static: a, PeerStatic: b.Public, PSK: psk, Prologue: prologue, Random: bytes.NewReader(bytes.Repeat([]byte{3}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	if wrongPSK {
		psk = bytes.Repeat([]byte{6}, 32)
	}
	peerStatic := a.Public
	if pattern == "IKpsk2" {
		peerStatic = nil
	}
	r, err := NewNoise(NoiseConfig{Pattern: pattern, Static: b, PeerStatic: peerStatic, PSK: psk, Prologue: prologue, Random: bytes.NewReader(bytes.Repeat([]byte{4}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	return i, r, v
}
func TestRemoteNoise(t *testing.T) {
	var vectors []noiseVector
	for _, pattern := range []string{"IKpsk2", "KK"} {
		t.Run(pattern, func(t *testing.T) {
			// Nonempty and empty prologues/payloads exercise both AEAD boundaries.
			for _, prologue := range [][]byte{[]byte("partyline remote v1"), nil} {
				i, r, v := noisePeers(t, pattern, false, prologue)
				var ic1, ic2, rc1, rc2 *noise.CipherState
				payloads := [][]byte{[]byte("phone hello"), []byte("machine hello")}
				if len(prologue) == 0 {
					payloads = [][]byte{nil, nil}
				}
				for n, payload := range payloads {
					sender, reader := i, r
					name := "initiator"
					if n == 1 {
						sender, reader = r, i
						name = "responder"
					}
					msg, c1, c2, err := sender.WriteMessage(nil, payload)
					if err != nil {
						t.Fatal(err)
					}
					plain, d1, d2, err := reader.ReadMessage(nil, msg)
					if err != nil || !bytes.Equal(payload, plain) {
						t.Fatalf("handshake: %x %v", plain, err)
					}
					v.Handshake = append(v.Handshake, noiseMessage{name, hex.EncodeToString(payload), hex.EncodeToString(msg)})
					if n == 1 {
						rc1, rc2, ic1, ic2 = c1, c2, d1, d2
					}
				}
				if !bytes.Equal(i.ChannelBinding(), r.ChannelBinding()) {
					t.Fatal("channel binding differs")
				}
				if !bytes.Equal(r.PeerStatic(), noiseKey(t, 1).Public) {
					t.Fatal("initiator static differs")
				}
				v.Hash = hex.EncodeToString(i.ChannelBinding())
				transportPayloads := [][]byte{nil, []byte("machine output"), []byte("phone command"), []byte{0, 1, 255}, bytes.Repeat([]byte{42}, 1024)}
				// Exercise the little-endian nonce carry in both directions. Keep
				// this in one vector so the fixture stays reasonably small.
				if pattern == "KK" && len(prologue) == 0 {
					for len(transportPayloads) < 514 {
						transportPayloads = append(transportPayloads, []byte{byte(len(transportPayloads))})
					}
				}
				for n, payload := range transportPayloads {
					sender, reader := ic1, rc1
					name := "initiator"
					if n%2 == 1 {
						sender, reader = rc2, ic2
						name = "responder"
					}
					msg, err := sender.Encrypt(nil, nil, payload)
					if err != nil {
						t.Fatal(err)
					}
					plain, err := reader.Decrypt(nil, nil, msg)
					if err != nil || !bytes.Equal(plain, payload) {
						t.Fatalf("transport: %v", err)
					}
					if _, err = reader.Decrypt(nil, nil, msg); err == nil {
						t.Fatal("replay accepted")
					}
					v.Transport = append(v.Transport, noiseMessage{name, hex.EncodeToString(payload), hex.EncodeToString(msg)})
				}
				vectors = append(vectors, v)
			}
		})
	}
	t.Run("wrong PSK", func(t *testing.T) {
		i, r, _ := noisePeers(t, "IKpsk2", true, nil)
		msg, _, _, err := i.WriteMessage(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err = r.ReadMessage(nil, msg); err != nil {
			t.Fatal(err)
		}
		msg, _, _, err = r.WriteMessage(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err = i.ReadMessage(nil, msg); err == nil {
			t.Fatal("wrong PSK accepted")
		}
	})
	data, err := json.MarshalIndent(vectors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	const path = "../../relay/testdata/noise-vectors.json"
	// Explicit regeneration: UPDATE_NOISE_VECTORS=1 go test -run '^TestRemoteNoise$' ./internal/remote
	if os.Getenv("UPDATE_NOISE_VECTORS") == "1" {
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, data) {
		t.Fatal("Noise vectors drifted; regenerate with UPDATE_NOISE_VECTORS=1")
	}
}

func TestRemoteNoiseConfig(t *testing.T) {
	for _, c := range []NoiseConfig{{Pattern: "XX", Static: noiseKey(t, 1)}, {Pattern: "KK"}, {Pattern: "KK", Static: noiseKey(t, 1)}, {Pattern: "IKpsk2", Static: noiseKey(t, 1), PSK: []byte{1}}, {Pattern: "IKpsk2", Static: noiseKey(t, 1), PeerStatic: noiseKey(t, 2).Public, PSK: make([]byte, 32)}, {Pattern: "KK", Static: noiseKey(t, 1), PeerStatic: noiseKey(t, 2).Public, PSK: make([]byte, 32)}} {
		if _, err := NewNoise(c); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestRemoteNoiseTransportBoundaries(t *testing.T) {
	i, r, _ := noisePeers(t, "KK", false, nil)
	msg, _, _, err := i.WriteMessage(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = r.ReadMessage(nil, msg); err != nil {
		t.Fatal(err)
	}
	msg, _, _, err = r.WriteMessage(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, send, _, err := i.ReadMessage(nil, msg)
	if err != nil {
		t.Fatal(err)
	}
	// The wrapper preserves flynn/noise's nonce exhaustion semantics.
	send.SetNonce(noise.MaxNonce)
	if _, err = send.Encrypt(nil, nil, make([]byte, 65519)); err != nil {
		t.Fatal(err)
	}
	if _, err = send.Encrypt(nil, nil, nil); err != noise.ErrMaxNonce {
		t.Fatalf("exhaustion: %v", err)
	}
}
