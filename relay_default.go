package main

import (
	"os"
	"strings"
)

// relay_default.go — where the relay address comes from now that pppp.sh is gone.
//
// WHAT CHANGED. "pppp.sh:22" was compiled into every shipped binary as the default relay, in four
// places, and partyline.sh's production box published :22 solely to honour it. Both Hetzner boxes
// are being decommissioned and that hostname will stop resolving.
//
// It was already dead before the DNS goes: a relay validates sessions against its own control
// plane (RELAY_API), and partyline.sh is now a documentation site with no API. So the fallback
// could not have worked even with the box running — it would hang rather than refuse.
//
// WHERE THE RELAY COMES FROM INSTEAD. Your own instance. The self-host stack runs a relay
// container, registers it, and the control plane hands its endpoint back when a session is
// created. PARTYLINE_RELAY overrides that for anyone running a relay somewhere else, and
// `--relay host:port` overrides both for one command.
//
// UNSET IS NOW A REAL STATE, and it must fail LOUDLY rather than silently dial a host that cannot
// answer. A session with no relay still works over a direct address on the same network; what it
// cannot do is hand out a link that reaches someone elsewhere, and the operator has to be told
// that rather than left watching a join hang.

// relayFromEnv is the operator's explicit choice, if they made one. Empty means "ask the control
// plane", which is the normal path.
func relayFromEnv() string {
	return strings.TrimSpace(os.Getenv("PARTYLINE_RELAY"))
}

// noRelayNotice explains an absent relay in terms of the thing to go and look at. It names the
// instance, because on a self-hosted box the answer is nearly always that the relay container is
// not running or has not registered.
func noRelayNotice(base string) string {
	return "no relay is configured for " + base + ", so this session has no join link that works " +
		"from another network.\n" +
		"   Anyone on this network can still join with a direct address.\n" +
		"   Your instance runs its own relay: check it is up (docker compose ps relay) and that\n" +
		"   RELAY_ID and RELAY_SECRET are set in its .env. To point at a relay elsewhere, set\n" +
		"   PARTYLINE_RELAY=host:port or pass --relay host:port."
}
