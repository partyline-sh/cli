package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/flynn/noise"
	qrcode "github.com/skip2/go-qrcode"
	"partyline.sh/partyline/internal/remote"
	"partyline.sh/partyline/internal/theme"
)

type pairOffer struct {
	Relay   string `json:"relay"`
	Machine string `json:"machine"`
	Public  string `json:"public"`
	PSK     string `json:"psk"`
	Invite  string `json:"invite"`
	Route   string `json:"route"`
	Expires int64  `json:"expires"`
}
type pairPending struct {
	Offer     pairOffer `json:"offer"`
	Challenge []byte    `json:"challenge"`
}

func pairStatic() (noise.DHKey, error) {
	path := filepath.Join(relayDir(), "noise-static")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key, e := noise.DH25519.GenerateKeypair(rand.Reader)
		if e != nil {
			return key, e
		}
		if e = relayPrivateFile(path, append(key.Private, key.Public...)); e != nil {
			return key, e
		}
		return key, nil
	}
	if err != nil {
		return noise.DHKey{}, err
	}
	if len(b) != 64 {
		return noise.DHKey{}, errors.New("invalid machine Noise key")
	}
	return noise.DHKey{Private: b[:32], Public: b[32:]}, nil
}
func pairString(o pairOffer) string { b, _ := json.Marshal(o); return "PTLN-PAIR " + string(b) }
func pairQR(typed string) (string, error) {
	q, err := qrcode.New(typed, qrcode.Low)
	if err != nil {
		return "", err
	}
	pixels := q.Bitmap()
	var out strings.Builder
	for y := 0; y < len(pixels); y += 2 {
		for x := range pixels[y] {
			top := pixels[y][x]
			bottom := y+1 < len(pixels) && pixels[y+1][x]
			switch {
			case top && bottom:
				out.WriteString("█")
			case top:
				out.WriteString("▀")
			case bottom:
				out.WriteString("▄")
			default:
				out.WriteByte(' ')
			}
		}
		out.WriteByte('\n')
	}
	return out.String(), nil
}
func remoteMain(args []string) {
	// Internal popup entrypoint; the public command never prints an offer into a pane.
	if len(args) == 2 && args[0] == "pair" && args[1] == "--popup" {
		if err := remotePairPopup(); err != nil {
			fatal(err)
		}
		return
	}
	if len(args) == 1 && args[0] == "phones" {
		if err := printRemotePhones(); err != nil {
			fatal(err)
		}
		return
	}
	if len(args) == 2 && args[0] == "revoke" {
		if err := revokeRemotePhone(context.Background(), args[1]); err != nil {
			fatal(err)
		}
		return
	}
	if len(args) != 1 || args[0] != "pair" {
		fatal(errors.New("usage: ptln remote pair | phones | revoke <id>"))
		return
	}
	// A private popup owns the offer; no PSK is passed in argv or a pane.
	if err := tmuxCmd("display-popup", "-E", "-w", "100%", "-h", "100%", selfExe(), "remote", "pair", "--popup").Run(); err != nil {
		fatal(err)
	}
}
func remotePairPopup() error {
	c, t, err := loadRelay()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(relayDir(), "pair.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("a pairing popup is already open")
	}
	o, err := newPairOffer(context.Background(), c, t)
	if err != nil {
		return err
	}
	challenge := make([]byte, 32)
	if _, err = rand.Read(challenge); err != nil {
		return err
	}
	b, _ := json.Marshal(pairPending{Offer: o, Challenge: challenge})
	path := filepath.Join(relayDir(), "pair-pending")
	if err = relayPrivateFile(path, b); err != nil {
		return err
	}
	defer os.Remove(path)
	typed := pairString(o)
	qr, err := pairQR(typed)
	if err != nil {
		return err
	}
	ensureWatcher()
	return pairApprovalProgram(fmt.Sprintf("%s%s\x1b[0m\n%s\n\nPaste in the relay app. No phone is approved yet.\n", theme.QRContrast, qr, typed), c, t, challenge)
}

func pairRespond(route string, message []byte) ([]byte, error) {
	h, _, err := pairHandshake(route, message)
	if err != nil {
		return nil, err
	}
	reply, _, _, err := h.WriteMessage(nil, nil)
	return reply, err
}
func pairHandshake(route string, message []byte) (*noise.HandshakeState, []byte, error) {
	b, err := os.ReadFile(filepath.Join(relayDir(), "pair-pending"))
	if err != nil {
		return nil, nil, err
	}
	var p pairPending
	if err = json.Unmarshal(b, &p); err != nil {
		return nil, nil, err
	}
	if p.Offer.Route != route || time.Now().UnixMilli() >= p.Offer.Expires {
		return nil, nil, errors.New("pair offer unavailable")
	}
	// Consume before processing: retries require a fresh offer and invite.
	if err = os.Remove(filepath.Join(relayDir(), "pair-pending")); err != nil {
		return nil, nil, err
	}
	key, err := pairStatic()
	if err != nil {
		return nil, nil, err
	}
	psk, err := hex.DecodeString(p.Offer.PSK)
	if err != nil {
		return nil, nil, err
	}
	h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "IKpsk2", Static: key, PSK: psk})
	if err != nil {
		return nil, nil, err
	}
	if _, _, _, err = h.ReadMessage(nil, message); err != nil {
		return nil, nil, err
	}
	return h, p.Challenge, nil
}

// Only the independent invite digest and routing metadata leave this machine.
func newPairOffer(ctx context.Context, c relayConfig, token string) (pairOffer, error) {
	key, err := pairStatic()
	if err != nil {
		return pairOffer{}, err
	}
	psk, err := relayRandom(32)
	if err != nil {
		return pairOffer{}, err
	}
	invite, err := relayRandom(32)
	if err != nil {
		return pairOffer{}, err
	}
	route, err := relayRandom(16)
	if err != nil {
		return pairOffer{}, err
	}
	expires := time.Now().Add(10 * time.Minute).UnixMilli()
	digest := sha256.Sum256([]byte(invite))
	var result struct {
		Expires int64 `json:"expires"`
	}
	if err = relayPost(ctx, c.URL, "/device-invite", map[string]string{"id": c.MachineID, "token": token, "hash": hex.EncodeToString(digest[:]), "route": route}, &result); err != nil {
		return pairOffer{}, err
	}
	if result.Expires <= time.Now().UnixMilli() {
		return pairOffer{}, errors.New("relay returned an expired invite")
	}
	return pairOffer{c.URL, c.MachineID, hex.EncodeToString(key.Public), psk, invite, route, min(expires, result.Expires)}, nil
}
