package gatewayjoin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"
	"golang.org/x/crypto/blake2s"
)

// Test-only cipher instrumentation captures the initiator's final payload key
// and associated data. An attacker owns this key and can perform the same work;
// the production responder uses the unmodified Noise implementation.
type recordedCipherFunc struct{ last *recordedCipher }
type recordedCipher struct {
	noise.Cipher
	key      [32]byte
	nonce    uint64
	ad       []byte
	recorder *recordedCipherFunc
}

func (f *recordedCipherFunc) CipherName() string { return noise.CipherChaChaPoly.CipherName() }
func (f *recordedCipherFunc) Cipher(key [32]byte) noise.Cipher {
	return &recordedCipher{Cipher: noise.CipherChaChaPoly.Cipher(key), key: key, recorder: f}
}
func (c *recordedCipher) Encrypt(out []byte, n uint64, ad, plain []byte) []byte {
	c.nonce = n
	c.ad = bytes.Clone(ad)
	c.recorder.last = c
	return c.Cipher.Encrypt(out, n, ad, plain)
}
func comparisonCode(binding []byte) string {
	sum := sha256.Sum256(append([]byte("ptln join code v2"), binding...))
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1000000)
}

func TestGatewayRejectsFinalMessageGroundToTargetComparisonCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	gatewayKey, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		channel *Channel
		err     error
	}
	accepted := make(chan outcome, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			accepted <- outcome{err: err}
			return
		}
		defer c.CloseNow()
		ch, err := Accept(ctx, c, gatewayKey)
		accepted <- outcome{ch, err}
	}))
	defer server.Close()
	c, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	attackerKey, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordedCipherFunc{}
	h, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: noise.NewCipherSuite(noise.DH25519, recorder, noise.HashBLAKE2s),
		Pattern:     noise.HandshakeXX, Initiator: true, StaticKeypair: attackerKey,
		Prologue: []byte("ptln-private-gateway-join-v2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	request := Request{Machine: strings.Repeat("ab", 16), Name: "Committed name"}
	payload, _ := json.Marshal(request)
	commitment := joinCommitment(attackerKey.Public, nonce, payload)
	first, _, _, err := h.WriteMessage(nil, commitment[:])
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Write(ctx, websocket.MessageBinary, first); err != nil {
		t.Fatal(err)
	}
	second, err := read(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = h.ReadMessage(nil, second); err != nil {
		t.Fatal(err)
	}
	third, _, _, err := h.WriteMessage(nil, append(bytes.Clone(nonce), payload...))
	if err != nil {
		t.Fatal(err)
	}
	last := recorder.last
	cipher := noise.CipherChaChaPoly.Cipher(last.key)
	// Check the attack's transcript calculation against an actual Noise handshake.
	baseline := blake2s.Sum256(append(bytes.Clone(last.ad), third[48:]...))
	if !bytes.Equal(baseline[:], h.ChannelBinding()) {
		t.Fatal("attack did not reproduce the Noise transcript")
	}
	target := "314159" // fixed independently of this handshake and its commitment
	var forged []byte
	var attempts int
	for attempts = 1; attempts <= 20000000; attempts++ {
		request.Name = fmt.Sprintf("Ground name %d", attempts)
		candidate, _ := json.Marshal(request)
		reveal := append(bytes.Clone(nonce), candidate...)
		encrypted := cipher.Encrypt(nil, last.nonce, last.ad, reveal)
		binding := blake2s.Sum256(append(bytes.Clone(last.ad), encrypted...))
		if comparisonCode(binding[:]) == target {
			forged = append(bytes.Clone(third[:48]), encrypted...)
			break
		}
	}
	if forged == nil {
		t.Fatal("attack could not find a comparison-code collision")
	}
	t.Logf("matched target code %s in %d offline final-message attempts", target, attempts)
	if err = c.Write(ctx, websocket.MessageBinary, forged); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-accepted:
		if result.channel != nil || result.err == nil || !strings.Contains(result.err.Error(), "commitment does not match") {
			t.Fatalf("ground message was not rejected by commitment: %v", result.err)
		}
	case <-ctx.Done():
		t.Fatal("gateway failed to reject the forged join", ctx.Err())
	}
}

func TestJoinCommitmentBindsAllFinalMessageInputs(t *testing.T) {
	static := bytes.Repeat([]byte{1}, 32)
	nonce := bytes.Repeat([]byte{2}, 32)
	payload := []byte(`{"machine":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Computer"}`)
	commitment := joinCommitment(static, nonce, payload)
	reveal := append(bytes.Clone(nonce), payload...)
	if out, err := revealJoin(commitment[:], static, reveal); err != nil || !bytes.Equal(out, payload) {
		t.Fatal("valid reveal rejected", err)
	}
	for _, field := range []string{"static", "nonce", "name", "whitespace", "commitment"} {
		t.Run(field, func(t *testing.T) {
			key, r, digest := bytes.Clone(static), bytes.Clone(reveal), bytes.Clone(commitment[:])
			switch field {
			case "static":
				key[0] ^= 1
			case "nonce":
				r[0] ^= 1
			case "name":
				r[len(r)-3] ^= 1
			case "whitespace":
				r = append(r, ' ')
			case "commitment":
				digest[0] ^= 1
			}
			if _, err := revealJoin(digest, key, r); err == nil {
				t.Fatal("changed reveal accepted")
			}
		})
	}
}
