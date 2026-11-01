package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"partyline.sh/partyline/internal/relayserver"
)

// A gateway can be server-only while this computer keeps using a different
// relay for its own sessions. Its owner credential never replaces that client.
func loadRelayOwner() (relayConfig, string, error) {
	host, err := loadRelayHost(relayHostDir())
	if err != nil {
		if os.IsNotExist(err) {
			return loadRelay()
		}
		return relayConfig{}, "", err
	}
	c, err := newRelayConfig(host.Origin, host.Machine)
	if err != nil {
		return c, "", err
	}
	c.Hosting = "self-hosted"
	b, err := os.ReadFile(filepath.Join(relayHostDir(), "owner-token"))
	if os.IsNotExist(err) {
		old, token, e := loadRelay()
		if e == nil && old.MachineID == host.Machine && old.URL == host.Origin && relayserver.Digest(token) == host.Hash {
			return old, token, nil
		}
	}
	if err != nil {
		return c, "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) != 64 || relayserver.Digest(token) != host.Hash {
		return c, "", errors.New("private gateway owner credential is invalid")
	}
	return c, token, nil
}
