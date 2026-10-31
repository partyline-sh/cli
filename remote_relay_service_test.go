package main

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestServiceDefinitionsKeepArgumentsAndPrivacySeparate(t *testing.T) {
	mac, err := relayServiceSpecFor("darwin", "/Users/test", "/Applications/Partyline & tools/ptln", "/Users/test/private data", 501)
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(mac.Contents))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("invalid launchd XML", err)
		}
	}
	if !strings.Contains(mac.Contents, "Partyline &amp; tools") || !strings.Contains(mac.Contents, "<string>--data</string>") {
		t.Fatal("service command was not separated into arguments")
	}
	linux, err := relayServiceSpecFor("linux", "/home/test", "/opt/Partyline tools/ptln", "/home/test/100% ${USER} data", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(linux.Contents, `ExecStart="/opt/Partyline tools/ptln" relay serve --data "/home/test/100%% $${USER} data"`) {
		t.Fatal("systemd argument quoting or percent escaping is wrong")
	}
	if strings.Contains(linux.Contents, "Token") || !strings.Contains(linux.Contents, "UMask=0077") {
		t.Fatal("unit leaked credentials or lost private umask")
	}
	if !bytes.Contains([]byte(linux.Why), []byte("lingering")) {
		t.Fatal("server logout behavior not explained")
	}
}
