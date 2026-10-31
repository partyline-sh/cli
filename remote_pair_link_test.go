package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"partyline.sh/partyline/internal/remote"
)

// The pairing link, the keys derived from its secret and the four-digit code are worked out
// separately in Go and in the phone app (relay/src/pairing.ts); both are pinned to the same
// vectors, computed independently.
func TestPairLinkAndCodeVectors(t *testing.T) {
	o := pairOffer{Relay: "https://relay.test", Public: strings.Repeat("22", 32), Secret: strings.Repeat("33", 16)}
	if got, want := pairLink(o), "https://relay.test/#p=IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIzMzMzMzMzMzMzMzMzMzMz"; got != want {
		t.Errorf("pairLink:\n got %s\nwant %s", got, want)
	}
	psk, invite, route := pairDerive(o.Secret)
	if psk != "bc9ab9bf462be380ab6ba5a4ba213a3cc1267966156a419a1c707ed4320cc62d" || invite != "0ce907f7ca87a8fbd2bac6f7f99fb6adef1c5be0d281367dcc4af1694530c7e7" || route != "ac93c285392f9037ac8b833db070c830" {
		t.Errorf("pairDerive: %s %s %s", psk, invite, route)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if got := phonePairCode(key); got != "2492" {
		t.Errorf("phonePairCode = %s, want 2492", got)
	}
	if len(pairLink(o)) > 110 || strings.Contains(pairLink(o), "?") {
		t.Error("the link must stay short, with the secret in the fragment, which the browser does not send")
	}
}

// The popup shows one code at a time with its countdown, and a fresh one when it lapses.
func TestPairCodeRenews(t *testing.T) {
	n := 0
	renew := func() (string, time.Time, error) {
		n++
		return fmt.Sprintf("QR%d", n), time.Now().Add(pairCodeLife), nil
	}
	m := newGateModel(gateActions{list: func() []*loopRun { return nil }, allowed: func() error { return nil }, code: gateCode, tab: func(*loopRun) string { return "" }})
	m.remoteQR, m.remoteUntil, m.remoteRenew = "QR0", time.Now().Add(30*time.Second), renew
	view := gateANSI.ReplaceAllString(m.View().Content, "")
	if !strings.Contains(view, "QR0") || !strings.Contains(view, "New code in 30s") {
		t.Fatalf("code and countdown:\n%s", view)
	}
	next, _ := m.Update(remoteTick{})
	if m = next.(gateModel); m.remoteQR != "QR0" || n != 0 {
		t.Fatal("renewed a code that had not lapsed")
	}
	m.remoteUntil = time.Now().Add(-time.Second)
	next, _ = m.Update(remoteTick{})
	if m = next.(gateModel); m.remoteQR != "QR1" || n != 1 {
		t.Fatalf("a lapsed code was not renewed: %q", m.remoteQR)
	}
}

// The phone pairs with a name and its key only, as relay/src/pairing.ts sends them: no passkey.
func TestPairEnrollWithoutPasskey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	machine, err := pairStatic()
	if err != nil {
		t.Fatal(err)
	}
	phone, _ := noise.DH25519.GenerateKeypair(rand.Reader)
	psk, _, route := pairDerive(strings.Repeat("ab", 16))
	pending, _ := json.Marshal(pairPending{Offer: pairOffer{PSK: psk, Route: route, Expires: time.Now().Add(time.Minute).UnixMilli()}, Challenge: make([]byte, 32)})
	if err := relayPrivateFile(filepath.Join(relayDir(), "pair-pending"), pending); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(psk)
	h, _ := remote.NewNoise(remote.NoiseConfig{Pattern: "IKpsk2", Initiator: true, Static: phone, PeerStatic: machine.Public, PSK: key})
	msg, _, _, _ := h.WriteMessage(nil, nil)
	reply, session, err := beginPair(route, msg)
	if err != nil {
		t.Fatal(err)
	}
	_, send, _, err := h.ReadMessage(nil, reply)
	if err != nil {
		t.Fatal(err)
	}
	plain := fmt.Sprintf(`{"name":"iPhone","static":%q}`, base64.StdEncoding.EncodeToString(phone.Public))
	encrypted, _ := send.Encrypt(nil, nil, []byte(plain))
	p, err := session.enroll(relayConfig{URL: "https://relay.test", RPID: "relay.test"}, encrypted)
	if err != nil || p.Name != "iPhone" || p.Credential.ID != "" || phonePairCode(p.Static) != phonePairCode(phone.Public) {
		t.Fatalf("enrolled %+v, %v", p, err)
	}
}
