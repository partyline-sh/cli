package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Usage and rate limits a reviewer hit, per engine:model, with when each lifts
// (~/.partyline/limits.json). The messages matched are the engines' own: the real samples are in
// testdata/limits/.

// The unix form only straight after claude's own phrase, not after any pipe in the diagnostics.
var limitUnix = regexp.MustCompile(`(?:usage limit reached\||resets?\s+(?:at\s+)?)([0-9]{10})\b`)

// claude's newer wording: "You've hit your 5-hour limit · resets 5pm (America/Vancouver)".
var claudeHitLimit = regexp.MustCompile(`you(?:'|’)ve hit your [a-z0-9 -]*limit`)
var limitClock = regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)(?:\s*\(([^)]+)\))?`)

// reviewerLimit reports whether output is engine's limit message, and when the limit lifts: the
// reset the message gives, or 12 hours when it gives none or gives one already past or more than
// eight days away (claude's longest limit is weekly).
func reviewerLimit(engine, output string, now time.Time) (time.Time, bool) {
	reset, ok := limitReset(engine, output, now)
	if ok && (!reset.After(now) || reset.After(now.Add(8*24*time.Hour))) {
		reset = now.Add(12 * time.Hour).UTC()
	}
	return reset, ok
}

func limitReset(engine, output string, now time.Time) (time.Time, bool) {
	text := strings.ToLower(output)
	known := false
	switch engine {
	case "codex":
		known = strings.Contains(text, "selected model is at capacity") || strings.Contains(text, "you've hit your usage limit") || strings.Contains(text, "usage_limit_reached") || strings.Contains(text, "rate limit reached") || strings.Contains(text, "rate_limit_exceeded")
	case "claude":
		known = strings.Contains(text, "usage limit reached") || strings.Contains(text, "rate limit reached") || strings.Contains(text, "rate_limit_error") || claudeHitLimit.MatchString(text)
	}
	if !known {
		return time.Time{}, false
	}
	if m := limitUnix.FindStringSubmatch(text); m != nil {
		seconds, _ := strconv.ParseInt(m[1], 10, 64)
		return time.Unix(seconds, 0).UTC(), true
	}
	if m := limitClock.FindStringSubmatch(text); m != nil {
		loc := now.Location()
		if m[4] != "" {
			if zone, err := time.LoadLocation(outputZone(output)); err == nil {
				loc = zone
			}
		}
		local := now.In(loc)
		hour, _ := strconv.Atoi(m[1])
		minute, _ := strconv.Atoi(m[2])
		if hour >= 1 && hour <= 12 && minute < 60 {
			hour %= 12
			if m[3] == "pm" {
				hour += 12
			}
			reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
			if !reset.After(now) {
				reset = reset.AddDate(0, 0, 1)
			}
			return reset.UTC(), true
		}
	}
	return now.Add(12 * time.Hour).UTC(), true
}

func outputZone(output string) string {
	m := limitClock.FindStringSubmatch(output)
	if m == nil {
		return ""
	}
	return m[4]
}

// The file lock covers readers and writers across detached reviews; rename keeps the JSON whole.
func withReviewerLimits(fn func(map[string]time.Time) error) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	path := filepath.Join(stateDir(), "limits.json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	limits := map[string]time.Time{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(data, &limits); err != nil {
			return fmt.Errorf("limits: %w", err)
		}
	}
	if limits == nil {
		return fmt.Errorf("limits: expected an object")
	}
	return fn(limits)
}

// reviewerLimited is when model's recorded limit lifts, or zero when it is not limited now.
func reviewerLimited(model string, now time.Time) (time.Time, error) {
	var reset time.Time
	err := withReviewerLimits(func(limits map[string]time.Time) error {
		if limits[model].After(now) {
			reset = limits[model]
		}
		return nil
	})
	return reset, err
}

// recordReviewerLimit records that model is limited until reset.
func recordReviewerLimit(model string, reset time.Time) error {
	return withReviewerLimits(func(limits map[string]time.Time) error {
		limits[model] = reset
		data, err := json.MarshalIndent(limits, "", "  ")
		if err != nil {
			return err
		}
		path := filepath.Join(stateDir(), "limits.json")
		if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
			return err
		}
		return os.Rename(path+".tmp", path)
	})
}
