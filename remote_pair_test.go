package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	"partyline.sh/partyline/internal/remote"
)

func TestRemotePairTransport(t *testing.T) {
	t.Run("popup", func(t *testing.T) {
		log := fixtureFake(t)
		// No relay yet: the relay settings open, where it is deployed.
		remoteMain([]string{"pair"})
		if b, err := os.ReadFile(log); err != nil || !strings.Contains(string(b), "tmux --relay-popup") || strings.Contains(string(b), "remote pair --popup") {
			t.Fatalf("no relay: %s (%v)", b, err)
		}
		if err := saveRelay(relayConfig{URL: "https://relay.test", RPID: "relay.test", MachineID: strings.Repeat("11", 16)}, strings.Repeat("ab", 32)); err != nil {
			t.Fatal(err)
		}
		remoteMain([]string{"pair"})
		if b, err := os.ReadFile(log); err != nil || !strings.Contains(string(b), "display-popup") || !strings.Contains(string(b), "remote pair --popup") {
			t.Fatalf("popup invocation: %s (%v)", b, err)
		}
	})
	// The QR encoder and typed display receive exactly the same non-URL payload.
	o := pairOffer{Relay: "https://relay.test", Machine: strings.Repeat("11", 16), Public: strings.Repeat("22", 32), PSK: strings.Repeat("33", 32), Invite: strings.Repeat("44", 32), Route: strings.Repeat("55", 16), Expires: time.Now().Add(time.Minute).UnixMilli()}
	typed := pairString(o)
	if !strings.HasPrefix(typed, "PTLN-PAIR ") {
		t.Fatal(typed)
	}
	var decoded pairOffer
	if err := json.Unmarshal([]byte(strings.TrimPrefix(typed, "PTLN-PAIR ")), &decoded); err != nil || decoded != o {
		t.Fatal("typed fields differ", err)
	}
	qr, err := pairQR(typed)
	if err != nil || !strings.Contains(qr, "▀") {
		t.Fatal("QR unavailable", err)
	}
	// Decode the rendered half-blocks, including their two pixel rows, independently.
	rows := strings.Split(strings.TrimSuffix(qr, "\n"), "\n")
	width := len([]rune(rows[0]))
	img := image.NewGray(image.Rect(0, 0, width*4, len(rows)*8))
	for y, row := range rows {
		for x, r := range []rune(row) {
			for half := 0; half < 2; half++ {
				dark := r == '█' || (half == 0 && r == '▀') || (half == 1 && r == '▄')
				shade := uint8(255)
				if dark {
					shade = 0
				}
				for dy := 0; dy < 4; dy++ {
					for dx := 0; dx < 4; dx++ {
						img.SetGray(x*4+dx, (y*2+half)*4+dy, color.Gray{Y: shade})
					}
				}
			}
		}
	}
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	result, err := qrcode.NewQRCodeReader().Decode(bitmap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetText() != typed {
		t.Fatal("QR and typed fields differ")
	}
	// Exercise the actual watcher responder, including consumption and expiry.
	t.Setenv("HOME", t.TempDir())
	if err = os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	var received []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device-invite" {
			t.Error("unexpected relay request", r.URL)
		}
		received, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]int64{"expires": time.Now().Add(time.Hour).UnixMilli()})
	}))
	defer server.Close()
	oldHTTP := relayHTTP
	relayHTTP = server.Client()
	defer func() { relayHTTP = oldHTTP }()
	offer, err := newPairOffer(context.Background(), relayConfig{URL: server.URL, MachineID: o.Machine}, strings.Repeat("66", 32))
	if err != nil {
		t.Fatal(err)
	}
	if offer.PSK == offer.Invite || strings.Contains(string(received), offer.PSK) || strings.Contains(string(received), offer.Invite) {
		t.Fatal("relay received pairing material")
	}
	digest := sha256.Sum256([]byte(offer.Invite))
	if !strings.Contains(string(received), hex.EncodeToString(digest[:])) {
		t.Fatal("invite digest absent")
	}
	if offer.Expires > time.Now().Add(10*time.Minute).UnixMilli() {
		t.Fatal("relay extended PSK lifetime")
	}
	key, err := pairStatic()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	psk, _ := hex.DecodeString(o.PSK)
	save := func() {
		b, _ := json.Marshal(pairPending{Offer: o})
		if err := relayPrivateFile(filepath.Join(relayDir(), "pair-pending"), b); err != nil {
			t.Fatal(err)
		}
	}
	for _, wrong := range []bool{false, true} {
		save()
		secret := append([]byte(nil), psk...)
		if wrong {
			secret[0] ^= 1
		}
		h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "IKpsk2", Initiator: true, Static: phone, PeerStatic: key.Public, PSK: secret})
		if err != nil {
			t.Fatal(err)
		}
		msg, _, _, err := h.WriteMessage(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		reply, err := pairRespond(o.Route, msg)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, err = h.ReadMessage(nil, reply)
		if (err != nil) != wrong {
			t.Fatalf("wrong=%v err=%v", wrong, err)
		}
		if _, err = pairRespond(o.Route, msg); err == nil {
			t.Fatal("reused offer accepted")
		}
	}
	o.Expires = time.Now().Add(-time.Second).UnixMilli()
	save()
	if _, err = pairRespond(o.Route, nil); err == nil {
		t.Fatal("expired offer accepted")
	}
}
