package conformance

import (
	"flag"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/relaycontract"
)

var origin = flag.String("relay-origin", "", "local fixture relay to test (no production secrets)")

func TestRelayContract(t *testing.T) {
	if *origin == "" {
		t.Skip("pass -relay-origin to test the local Cloudflare fixture")
	}
	relaycontract.Run(t, relaycontract.Fixture{Origin: *origin, Machine: strings.Repeat("11", 16), Token: strings.Repeat("22", 32)})
}
