package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/fxamacker/cbor/v2"
	"partyline.sh/partyline/internal/remote"
)

func enrollmentFixture(t *testing.T, challenge, static []byte, origin, rpID string) (pairEnrollment, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	client := func(kind string) []byte {
		b, _ := json.Marshal(map[string]any{"type": kind, "challenge": base64.RawURLEncoding.EncodeToString(challenge), "origin": origin, "crossOrigin": false})
		return b
	}
	hash := sha256.Sum256([]byte(rpID))
	auth := append(hash[:], 5, 0, 0, 0, 0)
	id := []byte("phone credential")
	registration := append(append([]byte(nil), auth...), make([]byte, 16)...)
	registration[32] |= 0x40
	registration = append(registration, 0, byte(len(id)))
	registration = append(registration, id...)
	registration = append(registration, cose...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": registration})
	if err != nil {
		t.Fatal(err)
	}
	e := pairEnrollment{Name: "iPhone", Static: static, Registration: remote.Registration{ID: base64.RawURLEncoding.EncodeToString(id), ClientData: client("webauthn.create"), Attestation: att}, Assertion: remote.Assertion{ID: base64.RawURLEncoding.EncodeToString(id), ClientData: client("webauthn.get"), AuthenticatorData: auth}}
	signAssertion(t, &e.Assertion, key)
	return e, key
}
func signAssertion(t *testing.T, a *remote.Assertion, key *ecdsa.PrivateKey) {
	t.Helper()
	client := sha256.Sum256(a.ClientData)
	digest := sha256.Sum256(append(append([]byte(nil), a.AuthenticatorData...), client[:]...))
	var err error
	a.Signature, err = ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
}
func TestRemotePairApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	challenge := make([]byte, 32)
	_, _ = rand.Read(challenge)
	static := make([]byte, 32)
	_, _ = rand.Read(static)
	e, key := enrollmentFixture(t, challenge, static, "https://relay.test", "relay.test")
	credential, err := remote.ParseRegistration(e.Registration, challenge, "https://relay.test", "relay.test")
	if err != nil {
		t.Fatal(err)
	}
	if err = remote.VerifyAssertion(credential, e.Assertion, challenge, "https://relay.test", "relay.test"); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"origin", "rpIdHash", "UV", "UP", "signature", "type", "challenge", "credential", "crossOrigin"} {
		t.Run(fault, func(t *testing.T) {
			a := e.Assertion
			a.ClientData = append([]byte(nil), a.ClientData...)
			a.AuthenticatorData = append([]byte(nil), a.AuthenticatorData...)
			a.Signature = append([]byte(nil), a.Signature...)
			switch fault {
			case "origin":
				a.ClientData = []byte(strings.ReplaceAll(string(a.ClientData), "https://relay.test", "https://wrong.test"))
			case "rpIdHash":
				a.AuthenticatorData[0] ^= 1
			case "UV":
				a.AuthenticatorData[32] &^= 4
			case "UP":
				a.AuthenticatorData[32] &^= 1
			case "signature":
				a.Signature[len(a.Signature)-1] ^= 1
			case "type":
				a.ClientData = []byte(strings.ReplaceAll(string(a.ClientData), "webauthn.get", "webauthn.create"))
			case "challenge":
				a.ClientData = []byte(strings.ReplaceAll(string(a.ClientData), base64.RawURLEncoding.EncodeToString(challenge), strings.Repeat("a", 43)))
			case "credential":
				a.ID = "other"
			case "crossOrigin":
				a.ClientData = []byte(strings.ReplaceAll(string(a.ClientData), "false", "true"))
			}
			if fault != "signature" {
				signAssertion(t, &a, key)
			}
			if remote.VerifyAssertion(credential, a, challenge, "https://relay.test", "relay.test") == nil {
				t.Fatal("accepted invalid assertion")
			}
		})
	}
	// Registration must bind the actual credential ID and a valid COSE key.
	bad := e.Registration
	bad.ID = "other"
	if _, err = remote.ParseRegistration(bad, challenge, "https://relay.test", "relay.test"); err == nil {
		t.Fatal("wrong registration ID accepted")
	}
	if _, err = remote.ParseRegistration(e.Registration, challenge, "https://wrong.test", "relay.test"); err == nil {
		t.Fatal("wrong registration origin accepted")
	}
	tokens := map[string]string{}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		switch r.URL.Path {
		case "/device-token":
			tokens[b["phone"]] = strings.Repeat("ab", 32)
			_ = json.NewEncoder(w).Encode(map[string]string{"token": tokens[b["phone"]]})
		case "/device-revoke":
			delete(tokens, b["phone"])
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			t.Error("unexpected relay path", r.URL.Path)
		}
	}))
	defer server.Close()
	oldHTTP := relayHTTP
	relayHTTP = server.Client()
	defer func() { relayHTTP = oldHTTP }()
	c := relayConfig{URL: server.URL, RPID: "relay.test", MachineID: strings.Repeat("11", 16)}
	p := remotePhone{ID: strings.Repeat("33", 16), Name: e.Name, Static: static, Credential: credential, Fingerprint: phoneFingerprint(static)}
	nothing := func() {
		t.Helper()
		for _, name := range []string{"phones.json", "events.jsonl"} {
			if _, err := os.Stat(filepath.Join(relayDir(), name)); !os.IsNotExist(err) {
				t.Fatalf("%s exists before approval: %v", name, err)
			}
		}
		if requests != 0 || len(tokens) != 0 {
			t.Fatal("relay changed before approval")
		}
	}
	for _, approve := range []bool{false, true} {
		// The popup's own code path is the only path to issuing and saving.
		var pending *loopRun
		item := &remoteGateItem{Phone: p, Pair: true, Decide: func() error {
			_, err := approveRemotePhone(context.Background(), c, "machine-token", p)
			if err == nil {
				pending = nil
			}
			return err
		}, Refuse: func() { pending = nil }}
		pending = phoneGateRun(item)
		m := newGateModel(gateActions{list: func() []*loopRun {
			if pending != nil {
				return []*loopRun{pending}
			}
			return nil
		}, allowed: func() error { return nil }, code: func() string { return "4242" }, tab: func(*loopRun) string { return "" }})
		if approve {
			m = gateKeys(m, "a")
			nothing()
			m = gateKeys(m, "4", "2", "4", "2")
			if !strings.Contains(m.result, "confirmed") {
				t.Fatal(m.result)
			}
		} else {
			m = gateKeys(m, "a", "esc")
			nothing()
			m = gateKeys(m, "r")
			nothing()
		}
	}
	phones, err := remotePhones()
	if err != nil || len(phones) != 1 || len(tokens) != 1 {
		t.Fatalf("phones=%v tokens=%v err=%v", phones, tokens, err)
	}
	digest := sha256.Sum256([]byte(strings.Repeat("ab", 32)))
	if phones[0].TokenHash != hex.EncodeToString(digest[:]) {
		t.Fatal("wrong token hash")
	}
	for _, name := range []string{"phones.json", "events.jsonl"} {
		fi, err := os.Stat(filepath.Join(relayDir(), name))
		if err != nil || fi.Mode().Perm() != 0600 {
			t.Fatalf("%s mode: %v %v", name, fi, err)
		}
	}
	// loadRelay validates production origins; use the canonical test origin and route
	// requests to the TLS server without weakening production config validation.
	c.URL = "https://relay.test"
	if err = saveRelay(c, strings.Repeat("aa", 32)); err != nil {
		t.Fatal(err)
	}
	relayHTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "https"
		r.URL.Host = strings.TrimPrefix(server.URL, "https://")
		return server.Client().Transport.RoundTrip(r)
	})}
	if err = revokeRemotePhone(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	phones, err = remotePhones()
	if err != nil || len(phones) != 0 || len(tokens) != 0 {
		t.Fatalf("revoke: %v %v %v", phones, tokens, err)
	}
	events, err := os.ReadFile(filepath.Join(relayDir(), "events.jsonl"))
	if err != nil || strings.Count(string(events), "\n") != 2 || !strings.Contains(string(events), "revoked") {
		t.Fatalf("events: %s %v", events, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRemotePairApprovalTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	// Unix socket path limits are shorter than testing.T's temporary directory.
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(pairApprovalSocket())) })
	l, err := listenPairApproval()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	requests := make(chan remotePairRequest, 1)
	challenge := make([]byte, 32)
	go servePairApproval(ctx, l, relayConfig{URL: "https://relay.test", RPID: "relay.test"}, "", challenge, requests)
	p := remotePhone{ID: strings.Repeat("12", 16), Name: "Phone", Static: make([]byte, 32), Fingerprint: phoneFingerprint(make([]byte, 32))}
	enrollment, _ := enrollmentFixture(t, challenge, p.Static, "https://relay.test", "relay.test")
	enrollment.Name = p.Name
	if _, err = requestPairApproval(ctx, p, enrollment, challenge); err == nil {
		t.Fatal("timed out approval accepted")
	}
	<-ctx.Done()
	select {
	case request := <-requests:
		if err := request.Item.Decide(); err == nil {
			t.Fatal("expired popup decision accepted")
		}
	default:
		t.Fatal("timeout did not exercise a pending popup request")
	}
	for _, name := range []string{"phones.json", "events.jsonl"} {
		if _, err = os.Stat(filepath.Join(relayDir(), name)); !os.IsNotExist(err) {
			t.Fatal("timeout stored", name)
		}
	}
}

// Exercise the production Noise challenge, encrypted enrollment, local approval
// socket, popup confirmation, persistence and encrypted token response together.
func TestRemotePairApprovalHandshake(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	var issued int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device-token" {
			t.Error(r.URL.Path)
		}
		issued++
		_ = json.NewEncoder(w).Encode(map[string]string{"token": strings.Repeat("ab", 32)})
	}))
	defer server.Close()
	oldHTTP := relayHTTP
	relayHTTP = server.Client()
	defer func() { relayHTTP = oldHTTP }()
	c := relayConfig{URL: server.URL, RPID: "relay.test", MachineID: strings.Repeat("11", 16)}
	machine, err := pairStatic()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	psk := make([]byte, 32)
	_, _ = rand.Read(psk)
	o := pairOffer{PSK: hex.EncodeToString(psk), Route: strings.Repeat("12", 16), Expires: time.Now().Add(time.Minute).UnixMilli()}
	offerChallenge := make([]byte, 32)
	_, _ = rand.Read(offerChallenge)
	pending, _ := json.Marshal(pairPending{Offer: o, Challenge: offerChallenge})
	if err = relayPrivateFile(filepath.Join(relayDir(), "pair-pending"), pending); err != nil {
		t.Fatal(err)
	}
	h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "IKpsk2", Initiator: true, Static: phone, PeerStatic: machine.Public, PSK: psk})
	if err != nil {
		t.Fatal(err)
	}
	msg, _, _, err := h.WriteMessage(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reply, session, err := beginPair(o.Route, msg)
	if err != nil {
		t.Fatal(err)
	}
	payload, send, receive, err := h.ReadMessage(nil, reply)
	if err != nil {
		t.Fatal(err)
	}
	var challenge struct {
		Challenge []byte `json:"challenge"`
	}
	if json.Unmarshal(payload, &challenge) != nil || len(challenge.Challenge) != 32 {
		t.Fatal("missing machine challenge")
	}
	e, _ := enrollmentFixture(t, challenge.Challenge, phone.Public, c.URL, c.RPID)
	plain, _ := json.Marshal(e)
	encrypted, err := send.Encrypt(nil, nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := session.enroll(c, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Fingerprint != phoneFingerprint(phone.Public) {
		t.Fatal("wrong fingerprint")
	}
	if _, err = session.enroll(c, encrypted); err == nil {
		t.Fatal("enrollment replay accepted")
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(pairApprovalSocket())) })
	l, err := listenPairApproval()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	requests := make(chan remotePairRequest, 1)
	go servePairApproval(ctx, l, c, "machine-token", offerChallenge, requests)
	injectPairSocketClients(t)
	type outcome struct {
		token string
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		token, err := requestPairApproval(ctx, candidate, session.enrollment, session.challenge)
		done <- outcome{token, err}
	}()
	var item *remoteGateItem
	select {
	case request := <-requests:
		item = request.Item
	case <-ctx.Done():
		t.Fatal("popup never received enrollment")
	}
	for _, file := range []string{"phones.json", "events.jsonl"} {
		if _, err = os.Stat(filepath.Join(relayDir(), file)); !os.IsNotExist(err) {
			t.Fatal("stored before approval", file)
		}
	}
	m := newGateModel(gateActions{list: func() []*loopRun { return nil }, allowed: func() error { return nil }, code: func() string { return "4242" }, tab: func(*loopRun) string { return "" }})
	updated, _ := m.Update(remotePairRequest{item})
	m = updated.(gateModel)
	if view := m.View().Content; !strings.Contains(view, candidate.Fingerprint) {
		t.Fatal("popup does not show fingerprint", view)
	}
	m = gateKeys(m, "a", "4", "2", "4", "2")
	if !strings.Contains(m.result, "confirmed") {
		t.Fatal(m.result)
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	b, _ := json.Marshal(map[string]any{"approved": true, "token": result.token, "fingerprint": candidate.Fingerprint})
	ciphertext, err := session.send.Encrypt(nil, nil, b)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := receive.Decrypt(nil, nil, ciphertext)
	if err != nil || string(decoded) != string(b) {
		t.Fatalf("token reply %s %v", decoded, err)
	}
	phones, err := remotePhones()
	if err != nil || len(phones) != 1 || issued != 1 {
		t.Fatalf("phones %v issued %d err %v", phones, issued, err)
	}
}

func TestRemotePairApprovalPersistenceFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	// A journal that cannot be opened must never leave a paired phone or token.
	if err := os.Mkdir(filepath.Join(relayDir(), "events.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	var live bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device-token":
			live = true
			_ = json.NewEncoder(w).Encode(map[string]string{"token": strings.Repeat("ab", 32)})
		case "/device-revoke":
			live = false
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer server.Close()
	oldHTTP := relayHTTP
	relayHTTP = server.Client()
	defer func() { relayHTTP = oldHTTP }()
	p := remotePhone{ID: strings.Repeat("12", 16), Name: "Phone", Static: make([]byte, 32)}
	if _, err := approveRemotePhone(context.Background(), relayConfig{URL: server.URL, MachineID: strings.Repeat("11", 16)}, "token", p); err == nil {
		t.Fatal("unwritable audit log accepted")
	}
	if live {
		t.Fatal("relay token was not rolled back")
	}
	if _, err := os.Stat(filepath.Join(relayDir(), "phones.json")); !os.IsNotExist(err) {
		t.Fatal("pairing saved without audit record")
	}
}

func TestRemotePairApprovalPopupAuthority(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, allowed := range []bool{false, true} {
		calls := 0
		p := remotePhone{ID: strings.Repeat("12", 16), Name: "Phone", Static: make([]byte, 32), Fingerprint: phoneFingerprint(make([]byte, 32))}
		item := &remoteGateItem{Phone: p, Pair: true, Decide: func() error { calls++; return nil }}
		m := newGateModel(gateActions{list: func() []*loopRun { return []*loopRun{phoneGateRun(item)} }, allowed: func() error {
			if !allowed {
				return errors.New("not the popup")
			}
			return nil
		}, code: func() string { return "4242" }, tab: func(*loopRun) string { return "" }})
		m = gateKeys(m, "a", "4", "2", "4", "2")
		if allowed && calls != 1 {
			t.Fatal("the code did not approve", m.result)
		}
		if !allowed && (calls != 0 || !strings.Contains(m.result, "refused")) {
			t.Fatal("an untrusted terminal approved", m.result)
		}
	}
	// Saved phone rows have no loop actions. They need confirmation to revoke,
	// and a refresh must retain the in-memory wrong-code limit.
	calls := 0
	makeRow := func() []*loopRun {
		return []*loopRun{phoneGateRun(&remoteGateItem{Phone: remotePhone{ID: strings.Repeat("12", 16), Name: "Saved"}, Decide: func() error { calls++; return nil }})}
	}
	m := newGateModel(gateActions{list: makeRow, allowed: func() error { return nil }, code: func() string { return "4242" }, tab: func(*loopRun) string { return "" }})
	m = gateKeys(m, "r", "v", "s", "x", "f", "d")
	if calls != 0 || m.mode != gateSummary {
		t.Fatal("phone row accepted a loop action")
	}
	m = gateKeys(m, "a", "0", "0", "0", "0")
	m = m.reloadKeepingSelection()
	if len(m.current().Remote.Misses) != 1 {
		t.Fatal("refresh lost failed confirmations")
	}
	m = gateKeys(m, "a")
	if calls != 0 {
		t.Fatal("revoked without confirmation")
	}
	m = gateKeys(m, "4", "2", "4", "2")
	if calls != 1 {
		t.Fatal("did not revoke after confirmation", m.result)
	}
}

func TestRemotePairApprovalRejectsOldOfferChallenge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(pairApprovalSocket())) })
	l, err := listenPairApproval()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	current := make([]byte, 32)
	_, _ = rand.Read(current)
	old := make([]byte, 32)
	p := remotePhone{ID: strings.Repeat("12", 16), Name: "Phone", Static: make([]byte, 32), Fingerprint: phoneFingerprint(make([]byte, 32))}
	proof, _ := enrollmentFixture(t, old, p.Static, "https://relay.test", "relay.test")
	proof.Name = p.Name
	requests := make(chan remotePairRequest, 1)
	go servePairApproval(ctx, l, relayConfig{URL: "https://relay.test", RPID: "relay.test"}, "", current, requests)
	if _, err = requestPairApproval(ctx, p, proof, old); err == nil {
		t.Fatal("old offer proof accepted")
	}
	if len(requests) != 0 {
		t.Fatal("old offer proof reached the approval UI")
	}
	for _, name := range []string{"phones.json", "events.jsonl"} {
		if _, err = os.Stat(filepath.Join(relayDir(), name)); !os.IsNotExist(err) {
			t.Fatal("replayed proof saved", name)
		}
	}
}
