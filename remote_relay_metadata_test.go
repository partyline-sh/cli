package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRemoteRelayMetadataContainsOnlyPublicConfiguredRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if info := remoteRelayMetadata(); info != nil {
		t.Fatal("invented route without configuration", info)
	}
	config, err := newRelayConfig("https://private.example.test", strings.Repeat("1", 32))
	if err != nil {
		t.Fatal(err)
	}
	config.Hosting = "self-hosted"
	token := strings.Repeat("2", 64)
	if err = saveRelay(config, token); err != nil {
		t.Fatal(err)
	}
	info := remoteRelayMetadata()
	if len(info) != 2 || info["origin"] != config.URL || info["hosting"] != "self-hosted" {
		t.Fatal("wrong configured route", info)
	}
	encoded, _ := json.Marshal(info)
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), config.MachineID) {
		t.Fatal("public metadata leaked a credential or machine identity")
	}
	config.Hosting = ""
	if err = saveRelay(config, token); err != nil {
		t.Fatal(err)
	}
	if _, present := remoteRelayMetadata()["hosting"]; present {
		t.Fatal("legacy configuration inferred a hosting provider")
	}
}
