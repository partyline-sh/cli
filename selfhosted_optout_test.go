package main

import (
	"testing"

	"partyline.sh/partyline/internal/api"
)

// A CLI pointed at someone else's control plane must make ZERO requests to partyline.sh. Both
// background reporters — the anonymous usage ping and the release/version check — are therefore
// gated on api.IsSelfHosted(), and these tests pin the three cases that matter: production is
// unchanged, self-hosted is off with no env vars at all, and the existing explicit opt-outs still
// work against production.

// clearOptOutEnv neutralises every env var either predicate reads, so a test asserts the base-URL
// behaviour and not the developer's shell (CI in particular is set in the release workflow).
func clearOptOutEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CI", "DO_NOT_TRACK", "PARTYLINE_TELEMETRY",
		"PARTYLINE_NO_UPDATE_CHECK", "NO_UPDATE_NOTIFIER",
	} {
		t.Setenv(k, "")
	}
}

// withRelease pretends this binary is a real release — both predicates skip dev builds, which would
// otherwise mask what we're testing.
func withRelease(t *testing.T) {
	t.Helper()
	prev := version
	version = "0.1.0"
	t.Cleanup(func() { version = prev })
}

func TestOptOutsPointedAtProduction(t *testing.T) {
	isolateInstance(t)
	clearOptOutEnv(t)
	withRelease(t)
	t.Setenv("PARTYLINE_API", "https://partyline.sh")

	if api.IsSelfHosted() {
		t.Fatal("partyline.sh must not be treated as self-hosted")
	}
	// TELEMETRY IS OFF THERE NOW, and that is the point rather than a regression. partyline.sh
	// serves documentation; the app and its database are gone, so the ping has nowhere to land.
	// The CLI used to announce an anonymous daily ping and then POST it at a documentation site.
	if !telemetryDisabled() {
		t.Error("telemetry must be off against partyline.sh — there is no API to receive it")
	}
	// Update checks STAY ON. /api/v1/version is not gated by marketing-only mode and answers
	// without touching the database, so it still works — which is how a v0.89.0 install on a
	// real box found 0.89.1. Turning this off would strand every installed CLI on its version.
	if updateChecksDisabled() {
		t.Error("update checks must keep working — /api/v1/version still answers")
	}
}

// The default (PARTYLINE_API unset) is production too — the literal moved to api.prodBase, so guard
// that the move didn't change what an unconfigured install does.
func TestOptOutsWithNoBaseConfigured(t *testing.T) {
	isolateInstance(t)
	clearOptOutEnv(t)
	withRelease(t)
	t.Setenv("PARTYLINE_API", "")

	if api.Base() != "https://partyline.sh" {
		t.Fatalf("default base = %q, want https://partyline.sh", api.Base())
	}
	// Unconfigured is the same case as partyline.sh, because that IS the default base: no
	// instance, so nothing to report to, but updates still resolve.
	if !telemetryDisabled() {
		t.Error("telemetry must be off with no instance configured")
	}
	if updateChecksDisabled() {
		t.Error("update checks must keep working with no instance configured")
	}
}

func TestOptOutsPointedAtSelfHostedInstance(t *testing.T) {
	isolateInstance(t)
	clearOptOutEnv(t)
	withRelease(t)
	t.Setenv("PARTYLINE_API", "https://pl.example.com")

	if !api.IsSelfHosted() {
		t.Fatal("a non-partyline.sh base must be treated as self-hosted")
	}
	if !telemetryDisabled() {
		t.Error("telemetryDisabled() = false on a self-hosted instance — it would phone home to us")
	}
	if !updateChecksDisabled() {
		t.Error("updateChecksDisabled() = false on a self-hosted instance — it would poll our release channel")
	}
}

// Self-hosted is not a preference: an operator who explicitly asked for telemetry still sends
// nothing, because the usage isn't ours to count.
func TestSelfHostedBeatsExplicitTelemetryOptIn(t *testing.T) {
	isolateInstance(t)
	clearOptOutEnv(t)
	withRelease(t)
	t.Setenv("PARTYLINE_API", "https://pl.example.com")
	t.Setenv("PARTYLINE_TELEMETRY", "1")

	if !telemetryDisabled() {
		t.Error("PARTYLINE_TELEMETRY=1 must not re-enable telemetry on a self-hosted instance")
	}
}

func TestExplicitOptOutsStillWorkAgainstProduction(t *testing.T) {
	isolateInstance(t)
	withRelease(t)

	t.Run("DO_NOT_TRACK", func(t *testing.T) {
		clearOptOutEnv(t)
		t.Setenv("PARTYLINE_API", "https://partyline.sh")
		t.Setenv("DO_NOT_TRACK", "1")
		if !telemetryDisabled() {
			t.Error("DO_NOT_TRACK must still disable telemetry")
		}
	})

	t.Run("PARTYLINE_TELEMETRY=0", func(t *testing.T) {
		clearOptOutEnv(t)
		t.Setenv("PARTYLINE_API", "https://partyline.sh")
		t.Setenv("PARTYLINE_TELEMETRY", "0")
		if !telemetryDisabled() {
			t.Error("PARTYLINE_TELEMETRY=0 must still disable telemetry")
		}
	})

	t.Run("PARTYLINE_NO_UPDATE_CHECK", func(t *testing.T) {
		clearOptOutEnv(t)
		t.Setenv("PARTYLINE_API", "https://partyline.sh")
		t.Setenv("PARTYLINE_NO_UPDATE_CHECK", "1")
		if !updateChecksDisabled() {
			t.Error("PARTYLINE_NO_UPDATE_CHECK must still disable the version check")
		}
	})
}
