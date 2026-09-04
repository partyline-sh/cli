package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// daemon_link.go — whether this machine is actually TALKING to its instance.
//
// THE LIE THIS REPLACES. The tray's headline read "Daemon: ● connected", and the value behind it
// was serviceActive() — `launchctl list <label>` exiting 0, i.e. "launchd has heard of this job".
// That is process registration, not connectivity. A daemon crash-looping, failing auth, or pointed
// at an endpoint that stopped listening reported "connected" the entire time. When an instance
// moved to a new hostname and every node went dark, the menu bar said connected throughout.
//
// A status light that is green while nothing works is worse than no light, because it sends you
// looking anywhere except the actual fault.
//
// The daemon already beats every 60s and threw the result away (`_ = api.Heartbeat(...)`). That
// call IS the liveness signal — it is the same beat the fleet page's last_seen is built from — so
// recording its outcome costs one small write per minute and makes the truth locally readable.
//
// WHY A FILE, NOT A NETWORK CALL FROM THE TRAY. The tray shells `ptln state` every 4 seconds and is
// deliberately network-free (see invites_cache.go for the same reasoning). A tray that dialled the
// control plane to ask "am I connected?" would answer "no" for its own timeouts, hammer the server
// once per user per 4s, and freeze its own menu on a slow link.

// linkStatePath is where the daemon records how its last beat went. Beside the device credential,
// per endpoint — a machine pointed at two instances has two independent link states, and mixing
// them would report one instance's health under the other's name.
func linkStatePath() string { return filepath.Join(daemonDir(), "link.json") }

// linkState is the last thing the daemon knows about its own connection.
type linkState struct {
	// Base is the endpoint the beat was addressed to. Recorded because "connected" means nothing
	// without saying to WHAT — the failure this whole file exists for was a daemon happily beating
	// at an address the operator had moved away from.
	Base string `json:"base"`

	OKAt  string `json:"ok_at,omitempty"`  // last beat the server accepted
	TryAt string `json:"try_at,omitempty"` // last beat attempted, success or not
	Err   string `json:"err,omitempty"`    // why the last beat failed; empty when it worked
}

// recordBeat writes the outcome of one heartbeat.
//
// Best-effort by design: a daemon that cannot write this file must keep beating. Losing the
// indicator is a cosmetic failure; stopping the heartbeat over it would be a real one.
func recordBeat(base string, err error, now time.Time) {
	st := linkState{Base: base, TryAt: now.UTC().Format(time.RFC3339)}
	if prev, ok := readLinkState(); ok {
		st.OKAt = prev.OKAt // keep the last SUCCESS across failures — "last seen 4m ago" needs it
	}
	if err == nil {
		st.OKAt = st.TryAt
	} else {
		// Bounded: a transport error can carry a whole URL and a chain of wrapped causes, and this
		// string is rendered in a menu bar.
		st.Err = trimLinkErr(err.Error())
	}
	b, mErr := json.Marshal(st)
	if mErr != nil {
		return
	}
	tmp := linkStatePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, linkStatePath()) // atomic: `ptln state` reads this on its own schedule
}

// linkErrMax is how much of a failure reason reaches the menu bar, in RUNES.
const linkErrMax = 120

// trimLinkErr makes an error safe to render on one line of a menu.
//
// Counts runes, not bytes: a transport error can carry a hostname with non-ASCII in it, and slicing
// a byte offset mid-sequence yields invalid UTF-8 — which a menu renders as a replacement glyph, or
// drops. Truncating a diagnostic into mojibake is a poor way to explain an outage.
func trimLinkErr(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > linkErrMax {
		return string(r[:linkErrMax-1]) + "…"
	}
	return s
}

func readLinkState() (linkState, bool) {
	b, err := os.ReadFile(linkStatePath())
	if err != nil {
		return linkState{}, false
	}
	var st linkState
	if json.Unmarshal(b, &st) != nil {
		return linkState{}, false
	}
	return st, true
}

// linkStaleAfter is how long without an accepted beat before the link is no longer "connected".
//
// Two and a half beats. One missed beat is a blip on any real network and flapping the light for it
// would train you to ignore it; three minutes of silence is a genuine problem worth showing.
const linkStaleAfter = 150 * time.Second

// linkHealth is the rendered answer: a state, and a phrase a human can act on.
type linkHealth struct {
	// Connected is the green light — a beat the server ACCEPTED, recently. Never derived from
	// whether a process exists.
	Connected bool   `json:"connected"`
	Detail    string `json:"detail"`             // "connected to partyline.example.com", "no reply for 6m — …"
	Since     string `json:"last_ok,omitempty"`  // RFC3339 of the last accepted beat
	Endpoint  string `json:"endpoint,omitempty"` // what it is talking to
}

// describeLink turns the recorded state into the line the tray and `ptln state` both show.
//
// `running` is whether the service process is up at all, and it is used ONLY to distinguish "the
// daemon is stopped" from "the daemon is running but cannot reach the instance" — two problems with
// completely different fixes that the old single line collapsed into one word.
func describeLink(running bool, now time.Time) linkHealth {
	st, ok := readLinkState()
	switch {
	case !running:
		return linkHealth{Detail: "stopped"}
	case !ok:
		// Running, but has never written a beat: either it just started, or it is an older build.
		return linkHealth{Detail: "starting — no beat yet"}
	}

	h := linkHealth{Since: st.OKAt, Endpoint: hostLabel(st.Base)}
	okAt, parseErr := time.Parse(time.RFC3339, st.OKAt)
	switch {
	case st.OKAt == "" || parseErr != nil:
		h.Detail = "cannot reach " + h.Endpoint
		if st.Err != "" {
			h.Detail += " — " + st.Err
		}
	case now.Sub(okAt) <= linkStaleAfter:
		h.Connected = true
		h.Detail = "connected to " + h.Endpoint
	default:
		// The important case, and the one that used to read as "connected": the process is alive
		// and the instance is not answering it.
		h.Detail = fmt.Sprintf("no reply from %s for %s", h.Endpoint, roughAge(now.Sub(okAt)))
		if st.Err != "" {
			h.Detail += " — " + st.Err
		}
	}
	return h
}

// hostLabel is the endpoint reduced to something that fits in a menu.
func hostLabel(base string) string {
	s := strings.TrimSpace(base)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return "the instance"
	}
	return s
}

// roughAge is a duration a person reads at a glance. Deliberately coarse: the difference between
// 4 and 5 minutes of silence changes nothing about what you do next.
func roughAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
