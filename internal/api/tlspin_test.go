package api

// The pin lifecycle against a REAL self-signed server: refused before the pin, fingerprint
// stable, accepted after the pin, and a DIFFERENT certificate refused again even with a pin
// on disk — the property that makes TOFU worth anything.
import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestPinLifecycleAgainstSelfSignedServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// 1. Unpinned: refused as unknown authority — the exact error TOFU answers.
	_, err := HTTPClient(2 * time.Second).Get(srv.URL)
	if !IsUnknownAuthority(err) {
		t.Fatalf("want unknown-authority refusal, got %#v / %v", err, err)
	}

	// 2. Fetch the chain the way login does, pin it.
	chain, err := FetchChain(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if fp := Fingerprint(chain[0]); len(fp) != 95 { // 32 bytes, colon-grouped
		t.Fatalf("fingerprint shape: %q", fp)
	}
	if err := SavePin(chain); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(pinPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("pin file: %v %v", fi, err)
	}

	// 3. Pinned: the same instance verifies.
	resp, err := HTTPClient(2 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("pinned connection refused: %v", err)
	}
	resp.Body.Close()

	// 4. A DIFFERENT self-signed server is still refused — the pin trusts one instance,
	//    not the concept of self-signed certificates. (httptest shares one static cert
	//    across NewTLSServer instances, so the second server needs its own, or this case
	//    silently tests nothing.)
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	other.TLS = &tls.Config{Certificates: []tls.Certificate{freshSelfSigned(t)}}
	other.StartTLS()
	defer other.Close()
	_, err = HTTPClient(2 * time.Second).Get(other.URL)
	if !IsUnknownAuthority(err) {
		t.Fatalf("a different certificate must still be refused, got %v", err)
	}
}

func freshSelfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "someone-else"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
