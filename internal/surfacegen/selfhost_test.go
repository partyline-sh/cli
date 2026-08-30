package surfacegen

import (
	"regexp"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/features"
)

// selfHostSection returns the generated setup section, failing the test rather than skipping if
// generation refuses — a refusal here means the registry and the prose have diverged.
func selfHostSection(t *testing.T) string {
	t.Helper()
	reg, err := features.Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	body, err := genSelfHost(repoRoot, reg)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// The acceptance criterion, both directions. A feature declared but not documented strands the
// reader who came for it; a feature documented but not declared is a doc for something the code
// does not have. Neither can be allowed, and neither is visible without this test.
func TestEveryRegistryFeatureIsDocumented(t *testing.T) {
	reg, err := features.Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	doc := selfHostSection(t)
	for _, f := range reg.Features {
		if !strings.Contains(doc, "(`"+f.Key+"`)") {
			t.Errorf("feature %q is in features.json but not in the generated setup section", f.Key)
		}
		for _, v := range f.Env {
			if !strings.Contains(doc, "`"+v+"`") {
				t.Errorf("feature %q declares %s, which the setup section never names", f.Key, v)
			}
		}
	}
	for key := range featureNotes {
		if _, ok := reg.Lookup(key); !ok {
			t.Errorf("surfacegen.featureNotes documents %q, which features.json does not declare", key)
		}
	}
}

// Every SHOUTY_NAME the section prints in backticks must be a variable the registry declares or the
// classification table knows about. This is what stops the prose from inventing a variable — a
// setup doc naming a variable nothing reads sends an operator to set something that changes
// nothing, which is the exact staleness deploy/stack/env.example already had.
func TestSetupSectionNamesNoUndeclaredVariable(t *testing.T) {
	reg, err := features.Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	doc := selfHostSection(t)
	shouty := regexp.MustCompile("`([A-Z][A-Z0-9_]{3,})`")
	for _, m := range shouty.FindAllStringSubmatch(doc, -1) {
		name := m[1]
		if reg.Declared(name) {
			continue
		}
		if _, ok := features.Classify(name); ok {
			continue
		}
		t.Errorf("the setup section names %s, which is neither declared in features.json nor classified in internal/features", name)
	}
}

// The compose facts a self-hoster needs before anything else. They are read from the real compose
// file rather than restated, so this asserts the reading worked, not that a sentence was typed.
func TestSetupSectionCarriesTheComposeFacts(t *testing.T) {
	doc := selfHostSection(t)
	for _, want := range []string{
		"ghcr.io/partyline-sh/",
		"compose stack",
		"apply-migrations.sh",
		"/opt/partyline/.env",
		"ptln server doctor",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the setup section never mentions %q", want)
		}
	}
	svcs, err := readComposeServices(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"postgres", "postgrest", "redis", "web", "relay", "caddy"} {
		found := false
		for _, got := range svcs {
			if got.Name == s {
				found = true
			}
		}
		if !found {
			t.Errorf("service %q is missing from the parsed compose services %v", s, svcs)
		}
		if !strings.Contains(doc, "`"+s+"`") {
			t.Errorf("the setup section never names the %q service", s)
		}
	}
}

// A published setup doc that leaked a value would be the worst possible artifact: it is fetched
// anonymously and pasted into models. Only NAMES and providers, never a value.
func TestSetupSectionCarriesNoValues(t *testing.T) {
	reg, err := features.Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	doc := selfHostSection(t)
	for _, f := range reg.Features {
		for _, v := range f.Env {
			if strings.Contains(doc, v+"=") {
				t.Errorf("the setup section assigns a value to %s — it must name variables, never set them", v)
			}
		}
	}
}

// Generation must REFUSE rather than emit a half-annotated section, in either direction.
func TestUndocumentedOrPhantomFeatureFailsGeneration(t *testing.T) {
	real, err := features.Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	undeclared := features.Registry{Features: []features.Feature{
		{Key: "brand_new_thing", Label: "Brand new thing", Env: []string{"BRAND_NEW_TOKEN"}, Docs: "x#y"},
	}}
	if err := checkNotesCoverRegistry(undeclared); err == nil {
		t.Error("a feature with no note passed generation")
	}
	// And the other direction: drop a real feature from the registry, leaving its note orphaned.
	trimmed := features.Registry{Features: append([]features.Feature(nil), real.Features[1:]...)}
	if err := checkNotesCoverRegistry(trimmed); err == nil {
		t.Errorf("an orphaned note for %q passed generation", real.Features[0].Key)
	}
}
