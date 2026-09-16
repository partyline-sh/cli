package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"partyline.sh/partyline/internal/api"
)

// invites_cache.go — session invites, from the control plane to the menu bar.
//
// WHY A CACHE FILE AND NOT A FETCH. `ptln state` is what the tray shells out to, every 4 seconds,
// and it is deliberately network-free: adding an API call there would be ~21,000 requests a day per
// machine to answer a question whose answer changes a handful of times a week. So the DAEMON — the
// process that already holds a persistent connection and is meant to be always-on — polls on a slow
// interval and writes here; `state` just reads the file, and stays a local, instant, offline-safe
// command.
//
// The cost of that split is honest and bounded: with no daemon running, the file goes stale and the
// tray shows nothing rather than something wrong (see inviteCacheTTL). An invite you can't see is a
// smaller failure than an invite that says "live" for a session that ended an hour ago.

// inviteCacheTTL bounds how old a cached list may be before we treat it as unknown. Comfortably
// longer than invitePollEvery so an ordinary slow tick never blanks the section, short enough that a
// daemon which stopped an hour ago isn't still advertising sessions that have since closed.
const inviteCacheTTL = 3 * time.Minute

// invitePollEvery is how often the daemon refreshes the list. Invites are a human-paced event; a
// tighter loop would spend requests to shave seconds off something nobody is staring at. The tray's
// own 4s tick still surfaces a new invite within one poll of it landing.
const invitePollEvery = 30 * time.Second

// inviteRow is one live session you were invited to and do not host. Everything here is display or
// navigation only — the join LINK carries the E2EE key in its fragment and is what actually admits
// you, so it is the one field that must survive the round trip intact.
type inviteRow struct {
	ID   string `json:"id"`
	Code string `json:"code,omitempty"` // join code — the fallback label, and what a human recognises
	Host string `json:"host,omitempty"` // who is hosting, resolved server-side; may be absent
	Link string `json:"link"`           // full join link, key fragment included
	// Since is the session's start time (RFC3339), which is the closest thing to "when you were
	// invited" the sessions list carries. Used only for ordering + an age hint, never for expiry.
	Since string `json:"since,omitempty"`
}

// Label is what the menu row and the notification say. Prefers the host's NAME — "darcy" is
// actionable, "cosmic-tiger-61" is a lookup — and falls back to the code when an older control
// plane didn't send one.
func (r inviteRow) Label() string {
	if r.Host != "" {
		return r.Host
	}
	return r.Code
}

type inviteCache struct {
	At   string      `json:"at"` // RFC3339 write time — the staleness check
	Rows []inviteRow `json:"rows"`
}

func inviteCachePath() string { return filepath.Join(daemonDir(), "invites.json") }

// readInviteCache returns the cached rows, or nil when the file is missing, unreadable, malformed,
// or older than the TTL. Every one of those is "we don't know", and they collapse to the same
// answer deliberately: showing a stale invite list is worse than showing none.
func readInviteCache(now time.Time) []inviteRow {
	b, err := os.ReadFile(inviteCachePath())
	if err != nil {
		return nil
	}
	var c inviteCache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil
	}
	at, err := time.Parse(time.RFC3339, c.At)
	if err != nil || now.Sub(at) > inviteCacheTTL {
		return nil
	}
	return c.Rows
}

// writeInviteCache replaces the cache atomically — the tray reads this file on its own schedule, and
// a torn read would show a half-written list as a malformed one (i.e. blank it for a tick).
func writeInviteCache(rows []inviteRow, now time.Time) error {
	b, err := json.Marshal(inviteCache{At: now.Format(time.RFC3339), Rows: rows})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(inviteCachePath()), 0o700); err != nil {
		return err
	}
	tmp := inviteCachePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, inviteCachePath())
}

// invitesFromSessions applies the SAME filter as `ptln join` with no link (joinPick): live, has a
// join link, and not one you host. One rule in two places would drift; this is the shared one, and
// it is a pure function so the filter is testable without a control plane.
func invitesFromSessions(ss []api.SessionInfo) []inviteRow {
	var rows []inviteRow
	for _, s := range ss {
		if s.Status != "live" || s.JoinLink == "" || s.IsHost {
			continue
		}
		rows = append(rows, inviteRow{
			ID:    s.ID,
			Code:  s.JoinCode,
			Host:  s.Host,
			Link:  s.JoinLink,
			Since: s.StartedAt,
		})
	}
	return rows
}

// refreshInviteCache does one poll. Called by the daemon loop; separated so a failure is a no-op on
// the existing file rather than a blank — a network blip must not retract an invite that is still
// open. Returns the rows it wrote (nil on failure) for the caller's logging.
func refreshInviteCache(c *api.Client, now time.Time) []inviteRow {
	if c.Token == "" {
		return nil // signed out: nothing to show, and nothing to ask for
	}
	ss, err := c.ListSessions()
	if err != nil {
		return nil
	}
	rows := invitesFromSessions(ss)
	if err := writeInviteCache(rows, now); err != nil {
		return nil
	}
	return rows
}
