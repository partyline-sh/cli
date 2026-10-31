package relayserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/flynn/noise"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

//go:embed public/*
var assets embed.FS

// AssetDigests binds a freshly initialized client to the browser code embedded
// in this binary, just as Cloudflare deployment records its served asset hashes.
func AssetDigests() map[string]string {
	out := map[string]string{}
	entries, _ := assets.ReadDir("public")
	for _, entry := range entries {
		b, _ := assets.ReadFile("public/" + entry.Name())
		h := sha256.Sum256(b)
		out["/"+entry.Name()] = hex.EncodeToString(h[:])
	}
	return out
}

type Config struct{ Database, Origin, InitialID, InitialHash, TrustedProxy string }
type Server struct {
	db             *sql.DB
	origin         string
	owner          string
	mu             sync.Mutex
	peers          map[*peer]struct{}
	requests       map[string]requestLimit
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	now            func() time.Time
	closed         bool
	joinKey        noise.DHKey
	joins          map[string]*pendingJoin
	joinSockets    map[*websocket.Conn]struct{}
	joining        int
	trustedProxies []netip.Prefix
}
type requestLimit struct {
	at    time.Time
	count int
}

func Open(c Config) (*Server, error) {
	u, err := url.Parse(c.Origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("relay origin must be an HTTPS origin")
	}
	db, err := openStore(c.Database, c.InitialID, c.InitialHash)
	if err != nil {
		return nil, err
	}
	proxies, err := parseTrustedProxy(c.TrustedProxy)
	if err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{db: db, origin: c.Origin, owner: c.InitialID, peers: make(map[*peer]struct{}), requests: make(map[string]requestLimit), ctx: ctx, cancel: cancel, now: time.Now}
	if err = s.initJoining(c.Database); err != nil {
		cancel()
		db.Close()
		return nil, err
	}
	s.trustedProxies = proxies

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.purge()
				s.mu.Lock()
				for ip, limit := range s.requests {
					if s.now().Sub(limit.at) > time.Minute {
						delete(s.requests, ip)
					}
				}
				s.mu.Unlock()
			}
		}
	}()
	return s, nil
}

func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	for c := range s.joinSockets {
		c.CloseNow()
	}
	for p := range s.peers {
		p.stop()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return s.db.Close()
}

func parseTrustedProxy(raw string) ([]netip.Prefix, error) {
	if raw == "none" {
		return nil, nil
	}
	if raw == "" {
		raw = "127.0.0.0/8,::1/128"
	}
	out := []netip.Prefix{}
	for _, part := range strings.Split(raw, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return nil, errors.New("trusted proxies must be comma-separated IP CIDRs, or none")
		}
		out = append(out, prefix.Masked())
	}
	return out, nil
}
func ValidateTrustedProxy(raw string) error { _, err := parseTrustedProxy(raw); return err }
func (s *Server) rateKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "invalid"
	}
	ip = ip.Unmap()
	trusted := false
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(ip) {
			trusted = true
			break
		}
	}
	if trusted {
		// Only the immediate proxy's appended/overwritten last hop is accepted.
		// Never use an untrusted client's leading X-Forwarded-For address.
		forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if parsed, err := netip.ParseAddr(strings.TrimSpace(forwarded[len(forwarded)-1])); err == nil {
			ip = parsed.Unmap()
		}
	}
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}
func (s *Server) proxyMode(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	ip = ip.Unmap()
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(ip) {
			parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
			if _, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return "forwarded"
			}
			return "shared"
		}
	}
	return "direct"
}
func (s *Server) allowHTTP(r *http.Request) bool {
	ip := s.rateKey(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.requests[ip]; !exists && len(s.requests) >= 4096 {
		var oldest string
		var at time.Time
		for key, b := range s.requests {
			if oldest == "" || b.at.Before(at) {
				oldest, at = key, b.at
			}
		}
		delete(s.requests, oldest)
	}
	b := s.requests[ip]
	if s.now().Sub(b.at) >= time.Minute {
		b = requestLimit{at: s.now()}
	}
	if b.count >= 120 {
		return false
	}
	b.count++
	s.requests[ip] = b
	return true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		reply(w, map[string]any{"ok": true, "protocol": 1, "hosting": "self-hosted"})
		return
	}
	if r.URL.Path == "/connect" {
		if !s.allowHTTP(r) {
			http.Error(w, "rate limited", 429)
			return
		}
		s.connect(w, r)
		return
	}
	if r.URL.Path == "/join-connect" {
		if !s.allowHTTP(r) {
			http.Error(w, "rate limited", 429)
			return
		}
		s.joinConnect(w, r)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		// Only serve the same fixed asset set as the Worker. No directory listings.
		if strings.Contains(path, "/") || strings.HasPrefix(path, ".") {
			http.NotFound(w, r)
			return
		}
		if _, err := assets.ReadFile("public/" + path); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		pub, _ := fs.Sub(assets, "public")
		http.FileServer(http.FS(pub)).ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodPost || !knownPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if !s.allowHTTP(r) {
		http.Error(w, "rate limited", 429)
		return
	}
	if o := r.Header.Get("Origin"); o != "" && o != s.origin {
		http.Error(w, "refused", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "too large", 413)
		return
	}
	var b struct {
		ID       string `json:"id"`
		Token    string `json:"token"`
		Invite   string `json:"invite"`
		Hash     string `json:"hash"`
		Route    string `json:"route"`
		Phone    string `json:"phone"`
		Request  string `json:"request"`
		Code     string `json:"code"`
		Approved bool   `json:"approved"`
	}
	if json.Unmarshal(body, &b) != nil {
		refuse(w)
		return
	}
	if r.URL.Path == "/join" {
		token, err := s.join(b.ID, b.Invite)
		if err != nil {
			refuse(w)
			return
		}
		reply(w, map[string]string{"token": token, "hosting": "self-hosted"})
		return
	}
	if !s.authenticated(b.ID, b.Token) {
		refuse(w)
		return
	}
	if (r.URL.Path == "/join-requests" || r.URL.Path == "/join-approve") && b.ID != s.owner {
		refuse(w)
		return
	}
	switch r.URL.Path {
	case "/join-requests":
		reply(w, map[string]any{"requests": s.pendingJoins()})
	case "/join-approve":
		if !s.approveJoin(b.Request, b.Code, b.Approved) {
			refuse(w)
			return
		}
		reply(w, map[string]bool{"ok": true})
	case "/invite":
		invite, expires, err := s.issueInvite("", "", "")
		if errors.Is(err, errInviteLimit) {
			http.Error(w, "invite limit", 429)
			return
		}
		if err != nil {
			refuse(w)
			return
		}
		reply(w, map[string]any{"invite": invite, "expires": expires})
	case "/status":
		s.mu.Lock()
		connected := false
		for p := range s.peers {
			if p.role == "machine" && p.id == b.ID {
				connected = true
				break
			}
		}
		s.mu.Unlock()
		pending := 0
		if b.ID == s.owner {
			pending = len(s.pendingJoins())
		}
		reply(w, map[string]any{"connected": connected, "hosting": "self-hosted", "protocol": 1, "owner": b.ID == s.owner, "pending_joins": pending, "proxy_mode": s.proxyMode(r)})
	case "/device-token":
		if !idPattern.MatchString(b.Phone) {
			refuse(w)
			return
		}
		token := randomSecret()
		if _, err = s.db.Exec("INSERT INTO devices(machine,phone,hash) VALUES(?,?,?)", b.ID, b.Phone, Digest(token)); err != nil {
			refuse(w)
			return
		}
		reply(w, map[string]any{"token": token, "machines": []string{b.ID}})
	case "/device-revoke":
		if !idPattern.MatchString(b.Phone) {
			refuse(w)
			return
		}
		// Registration and revocation use the same lock so an in-flight hello cannot
		// attach after the DELETE and escape revocation.
		s.mu.Lock()
		_, err = s.db.Exec("DELETE FROM devices WHERE machine=? AND phone=?", b.ID, b.Phone)
		if err == nil {
			for p := range s.peers {
				if p.role == "phone" && p.machine == b.ID && p.id == b.Phone {
					s.removeLocked(p)
				}
			}
		}
		s.mu.Unlock()
		if err != nil {
			refuse(w)
			return
		}
		reply(w, map[string]bool{"ok": true})
	case "/device-invite":
		if !secretPattern.MatchString(b.Hash) || !idPattern.MatchString(b.Route) || b.Route == b.ID {
			refuse(w)
			return
		}
		_, expires, err := s.issueInvite(b.ID, b.Hash, b.Route)
		if errors.Is(err, errInviteLimit) {
			http.Error(w, "invite limit", 429)
			return
		}
		if err != nil {
			refuse(w)
			return
		}
		reply(w, map[string]int64{"expires": expires})
	}
}
func knownPath(path string) bool {
	switch path {
	case "/invite", "/join", "/status", "/device-invite", "/device-token", "/device-revoke", "/join-requests", "/join-approve":
		return true
	}
	return false
}
func refuse(w http.ResponseWriter) { http.Error(w, "refused", 403) }
func reply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
