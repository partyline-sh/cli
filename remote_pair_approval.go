package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/flynn/noise"
	"partyline.sh/partyline/internal/remote"
)

type pairSession struct {
	receive, send *noise.CipherState
	static        []byte
	challenge     []byte
	expires       time.Time
	enrollment    pairEnrollment
}
type pairEnrollment struct {
	Name         string              `json:"name"`
	Static       []byte              `json:"static"`
	Registration remote.Registration `json:"registration"`
	Assertion    remote.Assertion    `json:"assertion"`
}

func beginPair(route string, message []byte) ([]byte, *pairSession, error) {
	h, challenge, err := pairHandshake(route, message)
	if err != nil {
		return nil, nil, err
	}
	if len(challenge) != 32 {
		return nil, nil, errors.New("pair offer has no challenge")
	}
	payload, _ := json.Marshal(map[string]any{"challenge": challenge})
	reply, receive, send, err := h.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, err
	}
	return reply, &pairSession{receive: receive, send: send, static: append([]byte(nil), h.PeerStatic()...), challenge: challenge, expires: time.Now().Add(2 * time.Minute)}, nil
}
func (s *pairSession) enroll(c relayConfig, message []byte) (remotePhone, error) {
	if time.Now().After(s.expires) {
		return remotePhone{}, errors.New("pairing expired")
	}
	plain, err := s.receive.Decrypt(nil, nil, message)
	if err != nil {
		return remotePhone{}, err
	}
	var e pairEnrollment
	if len(plain) > 32768 || json.Unmarshal(plain, &e) != nil || len(e.Name) == 0 || len(e.Name) > 80 || len(e.Static) != 32 || string(e.Static) != string(s.static) {
		return remotePhone{}, errors.New("invalid phone enrollment")
	}
	// A passkey is optional: pairing is the code compared on both screens and confirmed on the
	// machine. One sent anyway is still checked.
	var credential remote.Credential
	if e.Registration.ID != "" {
		var err error
		if credential, err = remote.ParseRegistration(e.Registration, s.challenge, c.URL, c.RPID); err != nil {
			return remotePhone{}, err
		}
		if err = remote.VerifyAssertion(credential, e.Assertion, s.challenge, c.URL, c.RPID); err != nil {
			return remotePhone{}, err
		}
	}
	id, err := relayRandom(16)
	if err != nil {
		return remotePhone{}, err
	}
	s.enrollment = e
	return remotePhone{ID: id, Name: e.Name, Static: e.Static, Credential: credential, Fingerprint: phoneFingerprint(e.Static)}, nil
}
func pairApprovalSocket() string {
	// sockaddr_un is only 104 bytes on macOS; HOME and TMPDIR can exceed that.
	digest := sha256.Sum256([]byte(relayDir()))
	return "/tmp/ptln-pair-" + hex.EncodeToString(digest[:16]) + "/approval.sock"
}

// Both endpoints refuse a pre-existing directory with unsafe ownership or mode.
func ensurePairApprovalDir() error {
	dir := filepath.Dir(pairApprovalSocket())
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode() != os.ModeDir|0700 || !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("unsafe pairing socket directory %s: require directory owned by uid %d with mode 0700 (no symlink)", dir, os.Getuid())
	}
	return nil
}
func listenPairApproval() (net.Listener, error) {
	if err := ensurePairApprovalDir(); err != nil {
		return nil, err
	}
	path := pairApprovalSocket()
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("pairing socket path %s is occupied by a non-socket", path)
		}
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("pairing socket %s is already in use", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("cannot check stale pairing socket: %w", dialErr)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

type pairApprovalRequest struct {
	Phone      remotePhone    `json:"phone"`
	Expires    time.Time      `json:"expires"`
	Enrollment pairEnrollment `json:"enrollment"`
	Challenge  []byte         `json:"challenge"`
}

// The challenge is established through pair-pending, independently of the socket.
// A fresh nonce prevents replay of a previous popup's proof. Never send the
// challenge or enrollment until the listener has proved possession.
func pairEndpointProof(challenge, nonce []byte) []byte {
	m := hmac.New(sha256.New, challenge)
	m.Write([]byte("ptln pairing popup v1"))
	m.Write(nonce)
	return m.Sum(nil)
}

func requestPairApproval(ctx context.Context, p remotePhone, enrollment pairEnrollment, challenge []byte) (string, error) {
	if err := ensurePairApprovalDir(); err != nil {
		return "", err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", pairApprovalSocket())
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if len(challenge) != 32 {
		return "", errors.New("invalid pairing challenge")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	_ = conn.SetDeadline(minTime(deadline, time.Now().Add(5*time.Second)))
	if _, err := conn.Write(nonce); err != nil {
		return "", fmt.Errorf("authenticate pairing popup: %w", err)
	}
	proof := make([]byte, 32)
	if _, err := io.ReadFull(conn, proof); err != nil {
		return "", fmt.Errorf("authenticate pairing popup: %w", err)
	}
	if !hmac.Equal(proof, pairEndpointProof(challenge, nonce)) {
		return "", errors.New("unauthenticated pairing popup: socket may be squatted")
	}
	_ = conn.SetDeadline(deadline)
	if err = json.NewEncoder(conn).Encode(pairApprovalRequest{Phone: p, Expires: deadline, Enrollment: enrollment, Challenge: challenge}); err != nil {
		return "", err
	}
	var result struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(io.LimitReader(conn, 1024)).Decode(&result)
	if err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", errors.New("pairing refused")
	}
	return result.Token, nil
}

type remoteGateItem struct {
	Phone   remotePhone
	Pair    bool
	Expires time.Time
	Misses  []time.Time
	Decide  func() error
	Refuse  func()
}
type remotePairRequest struct{ Item *remoteGateItem }

func phoneGateRun(item *remoteGateItem) *loopRun {
	return &loopRun{ID: "phone-" + item.Phone.ID, State: loopAwaiting, Remote: item}
}
func savedPhoneGateRuns() []*loopRun {
	phones, err := remotePhones()
	if err != nil {
		return nil
	}
	var out []*loopRun
	for _, p := range phones {
		out = append(out, phoneGateRun(&remoteGateItem{Phone: p, Decide: func() error { return revokeRemotePhone(context.Background(), p.ID) }}))
	}
	return out
}
func servePairApproval(ctx context.Context, l net.Listener, c relayConfig, token string, challenge []byte, requests chan<- remotePairRequest) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	slots := make(chan struct{}, 8)
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		select {
		case slots <- struct{}{}:
			go func() {
				released := false
				release := func() {
					if !released {
						<-slots
						released = true
					}
				}
				defer release()
				handlePairApproval(ctx, conn, c, token, challenge, requests, release)
			}()
		default:
			conn.Close()
		}
	}
}
func handlePairApproval(ctx context.Context, conn net.Conn, c relayConfig, token string, challenge []byte, requests chan<- remotePairRequest, validated func()) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(conn, nonce); err != nil || len(challenge) != 32 {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(pairEndpointProof(challenge, nonce)); err != nil {
		return
	}
	var request pairApprovalRequest
	if json.NewDecoder(io.LimitReader(conn, 32769)).Decode(&request) != nil {
		return
	}
	if len(challenge) != 32 || !bytes.Equal(challenge, request.Challenge) {
		return
	}
	if !request.Expires.After(time.Now()) || request.Expires.After(time.Now().Add(2*time.Minute)) {
		return
	}
	ctx, cancel := context.WithDeadline(ctx, request.Expires)
	defer cancel()
	p := request.Phone
	// The local socket is reachable by other processes under this user. Recheck
	// the proof in the process which owns approval, rather than trusting a
	// pre-parsed credential supplied by the caller.
	if len(p.Static) != 32 || p.Fingerprint != phoneFingerprint(p.Static) || len(p.Name) == 0 || len(p.Name) > 80 {
		return
	}
	if b, err := hex.DecodeString(p.ID); err != nil || len(b) != 16 {
		return
	}
	e := request.Enrollment
	if e.Name != p.Name || string(e.Static) != string(p.Static) {
		return
	}
	var credential remote.Credential
	if e.Registration.ID != "" {
		var err error
		credential, err = remote.ParseRegistration(e.Registration, request.Challenge, c.URL, c.RPID)
		if err != nil || remote.VerifyAssertion(credential, e.Assertion, request.Challenge, c.URL, c.RPID) != nil {
			return
		}
	}
	validated()
	_ = conn.SetDeadline(request.Expires)
	p.Credential = credential
	p.TokenHash = ""
	p.PairedAt = time.Time{}
	p.LastSeen = nil
	done := make(chan string, 1)
	item := &remoteGateItem{Phone: p, Pair: true, Expires: request.Expires}
	item.Decide = func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		device, err := approveRemotePhone(ctx, c, token, p)
		if err != nil {
			return err
		}
		done <- device
		return nil
	}
	item.Refuse = func() {
		select {
		case done <- "":
		default:
		}
	}
	select {
	case requests <- remotePairRequest{item}:
	case <-ctx.Done():
		return
	}
	select {
	case result := <-done:
		_ = json.NewEncoder(conn).Encode(map[string]string{"token": result})
	case <-ctx.Done():
	}
}
func pairApprovalProgram(renew func() (string, time.Time, error), c relayConfig, token string, challenge []byte) error {
	l, err := listenPairApproval()
	if err != nil {
		return err
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	requests := make(chan remotePairRequest)
	go servePairApproval(ctx, l, c, token, challenge, requests)
	m := newGateModel(gateActions{list: savedPhoneGateRuns, allowed: gateAllowed, tab: func(*loopRun) string { return "" }, code: gateCode})
	assetCtx, assetCancel := context.WithTimeout(ctx, 20*time.Second)
	if assetErr := relayCheckAssets(assetCtx, c); assetErr != nil {
		m.remoteAssets = "App assets: " + assetErr.Error()
	}
	assetCancel()
	qr, until, err := renew()
	if err != nil {
		return err
	}
	m.remoteQR, m.remoteUntil, m.remoteRenew = qr, until, renew
	m.remoteWait = func() tea.Msg {
		select {
		case r := <-requests:
			return r
		case <-ctx.Done():
			return tea.QuitMsg{}
		}
	}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
