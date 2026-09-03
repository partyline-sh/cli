package features

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure mode this whole package exists to prevent: a registry that cannot be read degrading
// to an empty one. An empty registry reports EVERY feature as not configured, which looks like a
// broken box and sends the operator to fix an environment that was fine.
func TestFeatureRegistryMalformedFailsLoudly(t *testing.T) {
	for name, body := range map[string]string{
		"not json":              `{`,
		"truncated":             `{"slack": {"label": "Slack", "env": ["A"]`,
		"empty object":          `{}`,
		"null feature":          `{"slack": null}`,
		"no label":              `{"slack": {"env": ["SLACK_CLIENT_ID"], "docs": "d"}}`,
		"no docs":               `{"slack": {"label": "Slack", "env": ["SLACK_CLIENT_ID"]}}`,
		"no env":                `{"slack": {"label": "Slack", "env": [], "docs": "d"}}`,
		"misspelled env key":    `{"slack": {"label": "Slack", "envs": ["SLACK_CLIENT_ID"], "docs": "d"}}`,
		"lowercase var":         `{"slack": {"label": "Slack", "env": ["slack_client_id"], "docs": "d"}}`,
		"duplicate var":         `{"slack": {"label": "Slack", "env": ["A", "A"], "docs": "d"}}`,
		"key is not snake case": `{"Slack App": {"label": "Slack", "env": ["A"], "docs": "d"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			reg, err := Parse([]byte(body))
			if err == nil {
				t.Fatalf("Parse(%s) returned no error; got %d features", name, len(reg.Features))
			}
			if len(reg.Features) != 0 {
				t.Errorf("Parse(%s) returned an error AND %d features — callers must get nothing", name, len(reg.Features))
			}
		})
	}
}

// The "misspelled env key" case above is the subtle one: without DisallowUnknownFields, `envs`
// decodes to an empty Env list and the feature reads as permanently CONFIGURED — a doctor that
// cheerfully reports Slack working on a box with no Slack credentials at all.
func TestFeatureRegistryNeverInventsAnAlwaysConfiguredFeature(t *testing.T) {
	reg, err := Parse([]byte(`{"slack": {"label": "Slack", "envs": ["SLACK_CLIENT_ID"], "docs": "d"}}`))
	if err == nil {
		for _, st := range reg.Status(func(string) string { return "" }) {
			if st.Configured {
				t.Fatalf("feature %q with no declared vars read as configured", st.Key)
			}
		}
		t.Fatal("a typo'd field name was accepted")
	}
}

func TestFeatureRegistryConfiguredState(t *testing.T) {
	reg, err := Parse([]byte(`{
	  "slack": {"label": "Slack", "env": ["SLACK_CLIENT_ID", "SLACK_SIGNING_SECRET"], "docs": "d"},
	  "redis": {"label": "Redis", "env": ["REDIS_URL"], "docs": "d"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"SLACK_CLIENT_ID":      "xoxb-not-a-real-value",
		"SLACK_SIGNING_SECRET": "also-not-real",
		"REDIS_URL":            "   ", // whitespace-only is UNSET: a trimmed-empty secret has never meant configured
	}
	byKey := map[string]Status{}
	for _, st := range reg.Status(func(k string) string { return env[k] }) {
		byKey[st.Key] = st
	}

	if !byKey["slack"].Configured {
		t.Errorf("slack: every var set but read as not configured (missing %v)", byKey["slack"].Missing)
	}
	if len(byKey["slack"].Missing) != 0 {
		t.Errorf("slack: configured but reported missing %v", byKey["slack"].Missing)
	}
	if byKey["redis"].Configured {
		t.Error("redis: whitespace-only value read as configured")
	}
	if got := byKey["redis"].Missing; len(got) != 1 || got[0] != "REDIS_URL" {
		t.Errorf("redis: missing = %v, want [REDIS_URL]", got)
	}

	// One var missing out of two is NOT CONFIGURED — two states, no partial.
	delete(env, "SLACK_SIGNING_SECRET")
	for _, st := range reg.Status(func(k string) string { return env[k] }) {
		if st.Key != "slack" {
			continue
		}
		if st.Configured {
			t.Error("slack: one var missing but still read as configured")
		}
		if len(st.Missing) != 1 || st.Missing[0] != "SLACK_SIGNING_SECRET" {
			t.Errorf("slack: missing = %v, want [SLACK_SIGNING_SECRET]", st.Missing)
		}
	}
}

// The registry that actually ships must parse. Without this, a malformed features.json would be
// caught for the first time by an operator on a box.
func TestFeatureRegistryOnDiskIsValid(t *testing.T) {
	reg, err := Load(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Features) == 0 {
		t.Fatal("features.json parsed to nothing")
	}
	for _, f := range reg.Features {
		if _, ok := Classify(f.Key); ok {
			t.Errorf("%s: a feature key must not collide with a classified var name", f.Key)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, Path)); err != nil {
		t.Fatalf("could not find %s from %s: %v", Path, root, err)
	}
	return root
}

func joined(errs []string) string { return strings.Join(errs, "\n  ") }
