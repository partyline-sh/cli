package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type relayConfig struct {
	Assets    map[string]string `json:"assets,omitempty"`
	URL       string            `json:"url"`
	RPID      string            `json:"rp_id"`
	MachineID string            `json:"machine_id"`
}

func relayDir() string { return filepath.Join(stateDir(), "remote") }
func relayRandom(n int) (string, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func relayURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Port() != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("relay URL must be an HTTPS origin")
	}
	return u, nil
}
func newRelayConfig(raw, id string) (relayConfig, error) {
	u, err := relayURL(raw)
	if err != nil {
		return relayConfig{}, err
	}
	if b, err := hex.DecodeString(id); err != nil || len(b) != 16 {
		return relayConfig{}, errors.New("invalid machine id")
	}
	u.Host = strings.ToLower(u.Host)
	return relayConfig{URL: strings.TrimSuffix(u.String(), "/"), RPID: u.Hostname(), MachineID: id}, nil
}
func loadRelay() (relayConfig, string, error) {
	var c relayConfig
	b, err := os.ReadFile(filepath.Join(relayDir(), "relay.json"))
	if err != nil {
		return c, "", err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, "", err
	}
	checked, err := newRelayConfig(c.URL, c.MachineID)
	if err != nil || checked.RPID != c.RPID {
		return c, "", errors.New("invalid relay configuration")
	}
	token, err := os.ReadFile(filepath.Join(relayDir(), "relay-token"))
	if err != nil {
		return c, "", err
	}
	t := strings.TrimSpace(string(token))
	decoded, err := hex.DecodeString(t)
	if err != nil || len(decoded) != 32 {
		return c, "", errors.New("invalid relay token")
	}
	return c, t, nil
}
func relayPrivateFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".relay-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func saveRelay(c relayConfig, token string) error {
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		return err
	}
	if err := os.Chmod(relayDir(), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err = relayPrivateFile(filepath.Join(relayDir(), "relay-token"), []byte(token+"\n")); err != nil {
		return err
	}
	return relayPrivateFile(filepath.Join(relayDir(), "relay.json"), append(b, '\n'))
}

var relayHTTP = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type relayHTTPError struct {
	Path   string
	Status int
}

func (e *relayHTTPError) Error() string { return fmt.Sprintf("relay %s: HTTP %d", e.Path, e.Status) }

func relayPost(ctx context.Context, origin, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", origin+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := relayHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return &relayHTTPError{path, res.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(out)
}
func relayInvite(ctx context.Context) (string, error) {
	c, t, err := loadRelay()
	if err != nil {
		return "", err
	}
	var result struct {
		Invite string `json:"invite"`
	}
	err = relayPost(ctx, c.URL, "/invite", map[string]string{"id": c.MachineID, "token": t}, &result)
	if err != nil {
		return "", err
	}
	return c.URL + "#" + result.Invite, nil
}
func relayJoin(ctx context.Context, invite string) error {
	if _, err := os.Stat(filepath.Join(relayDir(), "relay.json")); !os.IsNotExist(err) {
		return errors.New("this machine already has a relay configuration (or it cannot be read)")
	}
	origin, secret, ok := strings.Cut(invite, "#")
	if !ok {
		return errors.New("invalid invite")
	}
	id, err := relayRandom(16)
	if err != nil {
		return err
	}
	c, err := newRelayConfig(origin, id)
	if err != nil {
		return err
	}
	b, err := hex.DecodeString(secret)
	if err != nil || len(b) != 32 {
		return errors.New("invalid invite secret")
	}
	var result struct {
		Token string `json:"token"`
	}
	if err = relayPost(ctx, c.URL, "/join", map[string]string{"id": id, "invite": secret}, &result); err != nil {
		return err
	}
	b, err = hex.DecodeString(result.Token)
	if err != nil || len(b) != 32 {
		return errors.New("invalid machine token from relay")
	}
	return saveRelay(c, result.Token)
}
