package main

import "testing"

// The bug this pins: a provider block with NO resetsAt used to be discarded, because the parser
// required `blockedStatus && ResetsAt > 0`. Entitlement blocks — "usage credits are required for
// this model", model disabled for the org — are not time-windowed and carry no reset, so the entire
// class was invisible. The run died as a bare `exit status 1` with the real reason nowhere on
// screen, which is how a 2.7M-token run read as a mystery crash.
func TestParseRateLimitBlockWithoutReset(t *testing.T) {
	raw := []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","overageStatus":"org_level_disabled","message":"Usage credits are required for this model."}}`)
	reset, blocked, note := parseRateLimit(raw)
	if !blocked {
		t.Fatal("a rejected event with no resetsAt was not reported as blocked — this is the bug")
	}
	if !reset.IsZero() {
		t.Errorf("reset = %v, want zero (the event carried none; inventing one would show a bogus resume time)", reset)
	}
	if note != "Usage credits are required for this model." {
		t.Errorf("note = %q, want the provider's own wording", note)
	}
}

// The time-windowed case must keep working exactly as before.
func TestParseRateLimitBlockWithReset(t *testing.T) {
	raw := []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"exhausted","resetsAt":1770000000}}`)
	reset, blocked, _ := parseRateLimit(raw)
	if !blocked {
		t.Fatal("an exhausted event with a reset was not reported as blocked")
	}
	if reset.Unix() != 1770000000 {
		t.Errorf("reset = %d, want 1770000000", reset.Unix())
	}
}

// The regression that motivated the original resetsAt gate: `status:allowed` means the request WENT
// THROUGH, even when overage is rejected ("no overage left"). Pausing on it would halt healthy runs,
// so this must stay not-blocked — the fix above must not have widened the trigger.
func TestParseRateLimitAllowedIsNotABlock(t *testing.T) {
	raw := []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","overageStatus":"rejected"}}`)
	if _, blocked, _ := parseRateLimit(raw); blocked {
		t.Error("status:allowed was treated as a block — healthy runs would pause")
	}
}

// Anything that isn't a rate_limit_event is ignored outright.
func TestParseRateLimitIgnoresOtherEvents(t *testing.T) {
	for _, raw := range []string{`{"type":"assistant","message":{}}`, `not json`, `{}`} {
		if _, blocked, _ := parseRateLimit([]byte(raw)); blocked {
			t.Errorf("%q was treated as a rate-limit block", raw)
		}
	}
}
