package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// instance_probe.go — asking an instance who it is, over the wire.
//
// Split from instance_registry.go so the registry stays pure local state and is testable without a
// server, the same split as env.go/client.go.

// Identity is what /.well-known/partyline answers.
type Identity struct {
	InstanceID string `json:"instance_id"`
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
}

// identityProbeTimeout is short on purpose. Every caller has a correct answer for "no reply" — keep the
// mapping you already had — so a slow or absent instance must not hold up a login.
const identityProbeTimeout = 6 * time.Second

// ProbeInstance reads an instance's public identity.
//
// Uses the pinned client, so a self-hosted instance on its own CA is verified against the same pin
// as every other call rather than being a hole that skips it.
//
// An instance too old to serve the endpoint returns 404, which is not an error worth surfacing: it
// simply cannot vouch for itself yet, and the caller falls back to hostname-keyed config exactly as
// before. That is the whole upgrade story for existing installs — nothing breaks, and identity
// starts working the moment the server is updated.
func ProbeInstance(base string) (Identity, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return Identity{}, fmt.Errorf("no instance URL")
	}
	resp, err := HTTPClient(identityProbeTimeout).Get(base + "/.well-known/partyline")
	if err != nil {
		return Identity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("instance identity unavailable (HTTP %d)", resp.StatusCode)
	}
	// Bounded: this is parsed before anything is trusted, and an endpoint that streams forever
	// should not be able to exhaust the client's memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if err := json.Unmarshal(body, &id); err != nil {
		return Identity{}, fmt.Errorf("instance identity was not readable: %w", err)
	}
	return id, nil
}

// AdoptInstance probes `base` and files the result, returning what was learned.
//
// Called wherever a machine commits to an instance — login, and the daemon on connect. Best
// effort by contract: the error is for callers that want to SAY something, never a reason to
// refuse the command. A machine that cannot probe keeps working exactly as it did before this
// existed.
//
// The returned `moved` reports that this instance was already known at a DIFFERENT address — the
// case worth telling an operator about, because it means their fleet just followed a move and
// their old URL is dead.
func AdoptInstance(base string) (id Identity, moved bool, err error) {
	id, err = ProbeInstance(base)
	if err != nil || strings.TrimSpace(id.InstanceID) == "" {
		return id, false, err
	}
	// Read the previous sighting BEFORE filing this one, or the comparison is always against
	// what we just wrote.
	if rec, ok := KnownInstances()[id.InstanceID]; ok {
		prev := strings.TrimRight(rec.BaseURL, "/")
		moved = prev != "" && prev != strings.TrimRight(base, "/")
	}
	RememberInstance(base, id.InstanceID, id.Name)
	return id, moved, nil
}

// EnrollmentPrefix is the marker on a token that enrols a machine without a browser. Exported so
// the CLI can recognise one in an argument list rather than pattern-matching a literal.
const EnrollmentPrefix = "plt_enr_"
