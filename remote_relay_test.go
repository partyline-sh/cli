package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type relayFakeTransport struct {
	connections atomic.Int32
	hellos      chan []byte
	ack         func([]byte) []byte
	deadline    chan time.Duration
}

func (f *relayFakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/connect" {
		return nil, fmt.Errorf("unexpected request %s", req.URL)
	}
	if f.deadline != nil {
		deadline, ok := req.Context().Deadline()
		if !ok {
			f.deadline <- 0
		} else {
			f.deadline <- time.Until(deadline)
		}
	}
	a, b := net.Pipe()
	digest := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	headers := http.Header{"Upgrade": []string{"websocket"}, "Connection": []string{"Upgrade"}, "Sec-Websocket-Accept": []string{base64.StdEncoding.EncodeToString(digest[:])}}
	n := f.connections.Add(1)
	go func() {
		defer b.Close()
		_ = b.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(b)
		head := make([]byte, 2)
		if _, err := io.ReadFull(r, head); err != nil {
			return
		}
		size := int(head[1] & 127)
		if size == 126 {
			var extra [2]byte
			if _, err := io.ReadFull(r, extra[:]); err != nil {
				return
			}
			size = int(binary.BigEndian.Uint16(extra[:]))
		}
		if size > 4096 {
			return
		}
		mask := make([]byte, 4)
		if _, err := io.ReadFull(r, mask); err != nil {
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		for i := range body {
			body[i] ^= mask[i%4]
		}
		f.hellos <- body
		ack := append(append([]byte{}, body[:17]...), []byte(`{"ok":true,"version":1}`)...)
		if f.ack != nil {
			ack = f.ack(ack)
		}
		if _, err := b.Write(append([]byte{0x82, byte(len(ack))}, ack...)); err != nil {
			return
		}
		if n == 1 {
			return
		} // Drop the first authenticated socket, forcing watcher reconnect.
		_, _ = io.Copy(io.Discard, r)
	}()
	return &http.Response{StatusCode: 101, Header: headers, Body: a, Request: req}, nil
}
func TestRemoteRelay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// An unconfigured watcher must never dial, even though its relay loop is running.
	absent := &relayFakeTransport{hellos: make(chan []byte, 1)}
	priorHTTP := relayDialHTTP
	relayDialHTTP = &http.Client{Transport: absent, CheckRedirect: priorHTTP.CheckRedirect}
	absentCtx, stopAbsent := context.WithTimeout(context.Background(), 50*time.Millisecond)
	watchRelay(absentCtx)
	stopAbsent()
	relayDialHTTP = priorHTTP
	if absent.connections.Load() != 0 {
		t.Fatal("unconfigured watcher dialed relay")
	}
	id := strings.Repeat("ab", 16)
	token := strings.Repeat("cd", 32)
	for _, origin := range []string{"https://ptln-relay.person.workers.dev", "https://remote.example.org"} {
		c, err := newRelayConfig(origin, id)
		if err != nil {
			t.Fatal(err)
		}
		if c.RPID != strings.TrimPrefix(origin, "https://") {
			t.Fatalf("wrong RP ID: %s", c.RPID)
		}
		if err = saveRelay(c, token); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"relay.json", "relay-token"} {
			path := filepath.Join(relayDir(), name)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Fatalf("%s permissions: %o", name, info.Mode().Perm())
			}
		}
		data, err := os.ReadFile(filepath.Join(relayDir(), "relay.json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), token) || strings.Contains(string(data), "token") {
			t.Fatal("relay.json contains a token")
		}
		c.Assets = map[string]string{"/index.html": strings.Repeat("a", 64)}
		if err := saveRelay(c, token); err != nil {
			t.Fatal(err)
		}
		got, stored, err := loadRelay()
		if err != nil || !reflect.DeepEqual(got, c) || stored != token {
			t.Fatalf("roundtrip: %#v, %v", got, err)
		}
	}

	{
		data, err := relayDeploymentConfig(id, token)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), token) {
			t.Fatal("deploy config contains raw token")
		}
		var deployment struct {
			WorkersDev bool `json:"workers_dev"`
			Routes     []struct {
				Pattern      string `json:"pattern"`
				CustomDomain bool   `json:"custom_domain"`
			} `json:"routes"`
			Vars map[string]string `json:"vars"`
		}
		if err = json.Unmarshal(data, &deployment); err != nil {
			t.Fatal(err)
		}
		if !deployment.WorkersDev || len(deployment.Routes) != 0 {
			t.Fatal("the relay deploys on workers.dev only")
		}
		if deployment.Vars["INITIAL_MACHINE"] != id || len(deployment.Vars["INITIAL_HASH"]) != 64 {
			t.Fatal("missing bootstrap")
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	loginDir := filepath.Join(home, ".wrangler", "config")
	if err := os.MkdirAll(loginDir, 0700); err != nil {
		t.Fatal(err)
	}
	loginPath := filepath.Join(loginDir, "default.toml")
	if err := os.WriteFile(loginPath, []byte("send_metrics = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if relaySavedLogin() {
		t.Fatal("configuration without credentials reported as login")
	}
	if err := os.WriteFile(loginPath, []byte(`oauth_token = "test-login"`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !relaySavedLogin() {
		t.Fatal("saved Wrangler login was not detected")
	}
	if err := os.Remove(loginPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "  scoped-token \n")
	gotToken, typed, err := relayAPIToken()
	if err != nil || gotToken != "scoped-token" || typed {
		t.Fatal("API token was not trimmed")
	}
	// The environment wins over a saved token; with none in the environment, the saved one is used.
	if err := saveRelayAPIToken("saved-token"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(relayTokenPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved token mode: %v %v", info, err)
	}
	if gotToken, _, _ = relayAPIToken(); gotToken != "scoped-token" {
		t.Fatal("the saved token overrode CLOUDFLARE_API_TOKEN")
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	if gotToken, typed, err = relayAPIToken(); err != nil || gotToken != "saved-token" || typed {
		t.Fatalf("saved token not used: %q %v", gotToken, err)
	}
	if err := relayForgetToken(); err != nil || savedRelayAPIToken() != "" {
		t.Fatal("forget-token left the token")
	}
	fake := &relayFakeTransport{hellos: make(chan []byte, 4)}
	previous := relayDialHTTP
	relayDialHTTP = &http.Client{Transport: fake, CheckRedirect: previous.CheckRedirect}
	defer func() { relayDialHTTP = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { watchRelay(ctx); close(done) }()
	first := time.Now()
	for i := 0; i < 2; i++ {
		select {
		case hello := <-fake.hellos:
			if len(hello) < 17 || hello[0] != 1 {
				t.Fatal("hello is not an outer binary frame")
			}
			var body struct {
				Token string `json:"token"`
				Role  string `json:"role"`
			}
			if json.Unmarshal(hello[17:], &body) != nil || body.Token != token || body.Role != "machine" {
				t.Fatal("invalid hello")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("watcher did not connect and reconnect")
		}
	}
	if time.Since(first) < 450*time.Millisecond {
		t.Fatal("reconnect skipped backoff")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not cancel")
	}
	for attempt := 0; attempt < 30; attempt++ {
		d := relayBackoff(attempt)
		if d < 500*time.Millisecond || d > 5*time.Minute {
			t.Fatalf("backoff %v", d)
		}
	}
	dir := t.TempDir()
	if err := relayExtract(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err != nil {
		t.Fatal(err)
	}
}

type relayRoundTripFunc func(*http.Request) (*http.Response, error)

func (f relayRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRemoteRelayJoin(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		name, invite, response, want string
		status                       int
		existing                     bool
	}{
		{"existing", "https://relay.test#" + secret, "", "already has", 0, true},
		{"missing separator", "https://relay.test", "", "invalid invite", 0, false},
		{"bad secret", "https://relay.test#no", "", "invalid invite secret", 0, false},
		{"extra separator", "https://relay.test#" + secret + "#extra", "", "invalid invite secret", 0, false},
		{"forbidden", "https://relay.test#" + secret, "{}", "HTTP 403", 403, false},
		{"bad token", "https://relay.test#" + secret, `{"token":"zz"}`, "invalid machine token", 200, false},
		{"success", "https://relay.test#" + secret, `{"token":"` + secret + `"}`, "", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			old := relayHTTP
			client := *old
			calls := 0
			client.Transport = relayRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://relay.test/join" || r.Method != "POST" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["invite"] != secret || len(body["id"]) != 32 {
					t.Errorf("invalid join body: %v", body)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.response)), Header: make(http.Header)}, nil
			})
			relayHTTP = &client
			defer func() { relayHTTP = old }()
			if tc.existing {
				c, _ := newRelayConfig("https://relay.test", strings.Repeat("aa", 16))
				if err := saveRelay(c, secret); err != nil {
					t.Fatal(err)
				}
			}
			err := relayJoin(context.Background(), tc.invite)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want %s", err, tc.want)
				}
				if !tc.existing {
					if _, err := os.Stat(filepath.Join(relayDir(), "relay.json")); !os.IsNotExist(err) {
						t.Fatal("failed join saved config")
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				c, token, err := loadRelay()
				if err != nil || c.RPID != "relay.test" || token != secret {
					t.Fatalf("saved join: %v %v", c, err)
				}
			}
			if tc.status == 0 && calls != 0 {
				t.Fatal("invalid join sent request")
			}
		})
	}
}

func TestRemoteRelayBadAcknowledgment(t *testing.T) {
	if relayDialHTTP.Timeout != 0 {
		t.Fatal("WebSocket HTTP client has a lifetime timeout")
	}
	if relayDialHTTP.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("WebSocket redirects allowed")
	}
	for _, name := range []string{"version", "route"} {
		t.Run(name, func(t *testing.T) {
			fake := &relayFakeTransport{hellos: make(chan []byte, 1), deadline: make(chan time.Duration, 1)}
			fake.ack = func(ack []byte) []byte {
				if name == "route" {
					ack[1] ^= 0xff
					return ack
				}
				return append(ack[:17], []byte(`{"ok":true,"version":2}`)...)
			}
			old := relayDialHTTP
			client := *old
			client.Transport = fake
			relayDialHTTP = &client
			defer func() { relayDialHTTP = old }()
			c, _ := newRelayConfig("https://relay.test", strings.Repeat("aa", 16))
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			err := relaySession(ctx, c, strings.Repeat("bb", 32))
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("bad ack accepted: %v", err)
			}
			if d := <-fake.deadline; d <= 0 || d > 10*time.Second {
				t.Fatalf("unbounded dial: %v", d)
			}
		})
	}
}

func TestRemoteRelayInviteAndStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret := strings.Repeat("ab", 32)
	c, _ := newRelayConfig("https://relay.test", strings.Repeat("cd", 16))
	if err := saveRelay(c, secret); err != nil {
		t.Fatal(err)
	}
	old := relayHTTP
	client := *old
	client.Transport = relayRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["id"] != c.MachineID || body["token"] != secret {
			t.Fatal("missing credentials")
		}
		response := `{"invite":"` + secret + `"}`
		if r.URL.Path == "/status" {
			response = `{"connected":true}`
		} else if r.URL.Path != "/invite" {
			t.Fatal("unexpected path")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	relayHTTP = &client
	defer func() { relayHTTP = old }()
	invite, err := relayInvite(context.Background())
	if err != nil || invite != c.URL+"#"+secret {
		t.Fatalf("invite: %s %v", invite, err)
	}
	var status struct{ Connected bool }
	err = relayPost(context.Background(), c.URL, "/status", map[string]string{"id": c.MachineID, "token": secret}, &status)
	if err != nil || !status.Connected {
		t.Fatalf("status: %v %v", status, err)
	}
}

// The deployment parser intentionally accepts strict JSON. Keep comments or
// trailing commas in the embedded .jsonc from becoming a deploy-time surprise.
func TestRemoteRelayEmbeddedDeployment(t *testing.T) {
	if _, err := relayDeploymentConfig(strings.Repeat("ab", 16), strings.Repeat("cd", 32)); err != nil {
		t.Fatalf("embedded Wrangler config is not valid deployment JSON: %v", err)
	}
	var manifest struct{ DevDependencies map[string]string }
	b, err := relaySources.ReadFile("relay/deploy/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.DevDependencies) != 2 || manifest.DevDependencies["wrangler"] == "" || manifest.DevDependencies["esbuild"] == "" {
		t.Fatal("deployment must install only Wrangler and the PWA bundler")
	}
	testManifest, err := os.ReadFile("relay/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var tests struct{ DevDependencies map[string]string }
	if err = json.Unmarshal(testManifest, &tests); err != nil {
		t.Fatal(err)
	}
	if tests.DevDependencies["wrangler"] != manifest.DevDependencies["wrangler"] {
		t.Fatal("deployment and test Wrangler versions differ")
	}
	dir := t.TempDir()
	if err = relayExtract(dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"src/index.ts", "src/app.ts", "src/session.ts", "public/sw.js", "public/theme.css", "public/icon-192.png", "public/icon-512.png"} {
		if _, err = os.Stat(filepath.Join(dir, path)); err != nil {
			t.Fatal(err)
		}
	}
	b, err = os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "vitest") {
		t.Fatal("test toolchain embedded in deployment lockfile")
	}
}

func TestRemoteRelayOriginValidation(t *testing.T) {
	c, err := newRelayConfig("https://Remote.Example.ORG", strings.Repeat("1", 32))
	if err != nil || c.URL != "https://remote.example.org" || c.RPID != "remote.example.org" {
		t.Fatalf("normalization: %+v %v", c, err)
	}
	for _, tc := range []struct {
		output, want string
		fail         bool
	}{
		{"https://a.workers.dev https://a.workers.dev", "https://a.workers.dev", false},
		{"https://preview.workers.dev https://production.workers.dev", "", true},
		{"no URL", "", false},
	} {
		got, err := relayWorkersOrigin(tc.output)
		if got != tc.want || (err != nil) != tc.fail {
			t.Fatalf("origin %q: %q %v", tc.output, got, err)
		}
	}
}

func TestRemoteRelayDeployChecksLocalStateFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relayDir(), "relay.json"), []byte(`{"url":"https://relay.example.org","rp_id":"relay.example.org","machine_id":"11111111111111111111111111111111"}`), 0600); err != nil {
		t.Fatal(err)
	}
	err := relayDeploy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "existing relay configuration has no readable token") {
		t.Fatalf("expected local-state error before API token prompt: %v", err)
	}
}

// The deploy builds the phone app from what the binary embeds, so it must build the same way the
// repository does: the same build:pwa (including the shell-versioning step, without which a deploy
// ships a service worker that never replaces cached code) and every module the app imports.
func TestRemoteRelayDeployBuildsLikeTheRepo(t *testing.T) {
	read := func(b []byte) string {
		var m struct{ Scripts map[string]string }
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m.Scripts["build:pwa"]
	}
	deployed, err := relaySources.ReadFile("relay/deploy/package.json")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := os.ReadFile("relay/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := read(deployed), read(repo); got != want || !strings.Contains(got, "version-shell.mjs") {
		t.Fatalf("deploy build:pwa %q, repo %q", got, want)
	}
	if _, err := relaySources.ReadFile("relay/scripts/version-shell.mjs"); err != nil {
		t.Fatal("the shell-versioning script is not embedded")
	}
	// Every module the app's entry points import, directly or not, is embedded.
	importRE := regexp.MustCompile(`from\s+"\./([a-z0-9-]+)"`)
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		b, err := relaySources.ReadFile("relay/src/" + name + ".ts")
		if err != nil {
			t.Errorf("relay/src/%s.ts is imported but not embedded", name)
			return
		}
		for _, m := range importRE.FindAllStringSubmatch(string(b), -1) {
			walk(m[1])
		}
	}
	for _, entry := range []string{"pairing", "session", "app", "index"} {
		walk(entry)
	}
}
