// GitHub-key identity verification for SSH joiners (audit C1).
// A joiner connecting `ssh <github-user>@host` proves identity by presenting a
// key that matches the public keys published at github.com/<user>.keys. No
// signup, no shared secret — devs already have these keys loaded. Fails CLOSED:
// if GitHub is unreachable or the user/key is unknown, the connection is denied
// (use --insecure-any-key for trusted offline/LAN sessions).
package sshd

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// GitHub usernames: 1-39 chars, alphanumeric, hyphens allowed mid-name (not
// leading/trailing). RE2 has no lookahead; consecutive hyphens would just 404.
var ghUserRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

type keyCache struct {
	mu   sync.Mutex
	data map[string]cacheEntry
}
type cacheEntry struct {
	keys []string // marshaled authorized-key bytes as strings
	at   time.Time
}

var ghCache = &keyCache{data: map[string]cacheEntry{}}

const ghCacheTTL = 5 * time.Minute

// fetchGitHubKeys returns the marshaled public keys for a username (cached).
func fetchGitHubKeys(user string) ([]string, error) {
	if !ghUserRE.MatchString(user) {
		return nil, fmt.Errorf("invalid github username")
	}
	ghCache.mu.Lock()
	if e, ok := ghCache.data[user]; ok && time.Since(e.at) < ghCacheTTL {
		ghCache.mu.Unlock()
		return e.keys, nil
	}
	ghCache.mu.Unlock()

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get("https://github.com/" + user + ".keys")
	if err != nil {
		return nil, err // network error → caller fails closed
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	var keys []string
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		pk, _, _, _, err := gossh.ParseAuthorizedKey(line)
		if err != nil {
			continue
		}
		keys = append(keys, string(pk.Marshal()))
	}
	ghCache.mu.Lock()
	ghCache.data[user] = cacheEntry{keys: keys, at: time.Now()}
	ghCache.mu.Unlock()
	return keys, nil
}

// verifyGitHubKey reports whether `key` belongs to GitHub user `user`.
func verifyGitHubKey(user string, key ssh.PublicKey) bool {
	keys, err := fetchGitHubKeys(user)
	if err != nil {
		return false // fail closed
	}
	presented := string(key.Marshal())
	for _, k := range keys {
		if k == presented {
			return true
		}
	}
	return false
}
