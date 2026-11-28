package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelayServeRequiresSecureListener(t *testing.T) {
	base := relayHostConfig{Origin: "https://relay.test", Machine: strings.Repeat("1", 32), Listen: "127.0.0.1:8787"}
	if err := validateRelayHost(base); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"0.0.0.0:8787", ":8787", "192.168.1.2:8787", "localhost:8787"} {
		c := base
		c.Listen = address
		if validateRelayHost(c) == nil {
			t.Fatalf("allowed plaintext listener: %s", address)
		}
	}
	c := base
	c.Listen = "0.0.0.0:443"
	c.Cert = "cert.pem"
	c.Key = "key.pem"
	if err := validateRelayHost(c); err != nil {
		t.Fatal(err)
	}
	c.Key = ""
	if validateRelayHost(c) == nil {
		t.Fatal("accepted missing private key")
	}
	c = base
	c.ACME = true
	if validateRelayHost(c) == nil {
		t.Fatal("ACME allowed wrong port")
	}
}
func TestRelayServeInitNeverReplacesExistingClient(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(relayDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(relayDir(), "relay.json")
	original := []byte("existing client settings")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	err := relayServe(context.Background(), []string{"--init", "--origin", "https://relay.test", "--data", filepath.Join(dir, "private-server")})
	if err == nil || !strings.Contains(err.Error(), "will not replace") {
		t.Fatalf("did not refuse: %v", err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != string(original) {
		t.Fatal("replaced existing relay settings")
	}
}
