package main

import (
	"bytes"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/features"
)

// The registry compiled into the binary must be valid. If it is not, `ptln server doctor` on a box
// exits with a bug report instead of a state report — better than lying, but a build should never
// ship it.
func TestFeatureRegistryEmbeddedCopyIsValid(t *testing.T) {
	reg, err := features.Parse(featuresJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Features) == 0 {
		t.Fatal("the embedded registry has no features")
	}
	onDisk, err := features.Load(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk.Features) != len(reg.Features) {
		t.Errorf("embedded registry has %d features, features.json has %d", len(reg.Features), len(onDisk.Features))
	}
}

// THE GUARDRAIL. Doctor output exists to be pasted into an issue, so a value must never reach it —
// not in the human report, not in --json, not for a configured feature and not for a missing one.
func TestFeatureRegistryDoctorNeverPrintsAValue(t *testing.T) {
	reg, err := features.Parse(featuresJSON)
	if err != nil {
		t.Fatal(err)
	}
	// Every declared variable gets a distinct, unmistakable value.
	const secret = "SUPERSECRET-VALUE-"
	env := map[string]string{}
	for i, f := range reg.Features {
		for j, v := range f.Env {
			// Leave one feature's first var unset so both branches of the report are exercised.
			if i == 0 && j == 0 {
				continue
			}
			env[v] = secret + v
		}
	}
	look := func(k string) string { return env[k] }

	for _, asJSON := range []bool{false, true} {
		var b bytes.Buffer
		renderServerDoctor(&b, reg, look, asJSON)
		out := b.String()
		if strings.Contains(out, secret) {
			t.Fatalf("json=%v: doctor output contains an env var VALUE:\n%s", asJSON, out)
		}
		for _, v := range env {
			if strings.Contains(out, v) {
				t.Fatalf("json=%v: doctor output contains the value of a variable", asJSON)
			}
		}
	}
}

// Two states, and a not-configured feature must name what is missing and where to go next —
// otherwise the report costs the reader a turn to act on.
func TestFeatureRegistryDoctorNamesMissingVarsAndNextStep(t *testing.T) {
	reg, err := features.Parse([]byte(`{
	  "slack": {"label": "Slack app", "env": ["SLACK_CLIENT_ID", "SLACK_SIGNING_SECRET"], "docs": "deploy/stack/env.example#slack"},
	  "redis": {"label": "Redis", "env": ["REDIS_URL"], "docs": "deploy/stack/env.example#redis"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	look := func(k string) string {
		if k == "REDIS_URL" || k == "SLACK_CLIENT_ID" {
			return "set"
		}
		return ""
	}
	var b bytes.Buffer
	if allOK := renderServerDoctor(&b, reg, look, false); allOK {
		t.Error("reported everything configured while a var was unset")
	}
	out := b.String()
	for _, want := range []string{"SLACK_SIGNING_SECRET", "NOT configured", "next:", "deploy/stack/env.example#slack", "✓ redis"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output is missing %q:\n%s", want, out)
		}
	}
	// A configured feature must not be described as missing anything.
	if strings.Contains(out, "REDIS_URL") {
		t.Errorf("a configured feature's vars were listed as missing:\n%s", out)
	}
}
