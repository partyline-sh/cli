package api

// Self-hosted instances overwhelmingly serve Caddy's own internal CA (a LAN IP has no public
// certificate to get), and Go's client rightly refuses an unknown authority. The answer is
// the SSH answer: trust-on-first-use. On the first refusal, `ptln login` fetches the served
// chain, shows the leaf's SHA-256 fingerprint, and — with the human's explicit yes — pins the
// chain for THIS control plane (ConfigDir is per-endpoint). Every later connection verifies
// against system roots PLUS that pin, so a swapped certificate fails loudly again.
//
// Public-CA instances never see any of this: the system pool verifies them and no pin exists.

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func pinPath() string { return filepath.Join(ConfigDir(), "tls-pin.pem") }

// rootPool: system roots plus this endpoint's pinned chain, when one exists.
func rootPool() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if b, err := os.ReadFile(pinPath()); err == nil {
		pool.AppendCertsFromPEM(b)
	}
	return pool
}

// HTTPClient is the one constructor every API call should use: timeout plus the pin-aware
// trust store. With no pin on disk it behaves exactly like a default client.
func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: rootPool()},
		},
	}
}

// IsUnknownAuthority reports whether an error is the self-signed-instance refusal TOFU
// exists to answer.
func IsUnknownAuthority(err error) bool {
	if err == nil {
		return false
	}
	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) {
		return true
	}
	// Platform verifiers phrase the same refusal differently: Go's portable chain says
	// "signed by unknown authority"; darwin's Security.framework says "certificate is not
	// trusted". Hostname mismatches and expiry deliberately stay out — pinning must not
	// paper over a certificate for the wrong name.
	msg := err.Error()
	return strings.Contains(msg, "certificate signed by unknown authority") ||
		strings.Contains(msg, "certificate is not trusted")
}

// FetchChain grabs the certificate chain the endpoint is serving — verification off, because
// the entire point is that verification just failed. Nothing is trusted here; the chain is
// only material for a fingerprint and a human decision.
func FetchChain(base string) ([]*x509.Certificate, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		host += ":443"
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", host, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, fmt.Errorf("no certificate presented")
	}
	return certs, nil
}

// Fingerprint is the value a human compares: SHA-256 of the leaf, colon-grouped.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	out := make([]byte, 0, len(sum)*3)
	for i, b := range sum {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, fmt.Sprintf("%02X", b)...)
	}
	return string(out)
}

// SavePin persists the chain for this endpoint. 0600: it is a trust decision, not a secret,
// but nothing else needs to read it.
func SavePin(certs []*x509.Certificate) error {
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return err
	}
	var buf []byte
	for _, c := range certs {
		buf = append(buf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	return os.WriteFile(pinPath(), buf, 0o600)
}
