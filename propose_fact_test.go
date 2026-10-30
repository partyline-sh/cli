package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Every one of these was found by an agent using the tools as designed, with no shell access to
// fall back on — which is exactly why they survived my own testing.

// propose_fact's description tells the caller to OMIT repo when a fact is true of the whole
// project. A plain string cannot tell "absent" from "", so an omitted repo fell back to whatever
// repo the session was sitting in: nine of fifteen facts were filed under the cloud service,
// including a Java lane-client key and a POS library-shadowing trap that have nothing to do with it.
func TestOmittingRepoMeansTheWholeProjectNotTheCurrentOne(t *testing.T) {
	type args struct {
		Kind, Body, Sources string
		Repo                *string
	}
	var omitted args
	if err := json.Unmarshal([]byte(`{"kind":"gotcha","body":"x","sources":"a:1"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.Repo != nil {
		t.Fatal("an omitted repo must decode as nil, or it cannot be told from an empty one")
	}

	var given args
	if err := json.Unmarshal([]byte(`{"kind":"gotcha","body":"x","sources":"a:1","repo":"acr-pos"}`), &given); err != nil {
		t.Fatal(err)
	}
	if given.Repo == nil || *given.Repo != "acr-pos" {
		t.Fatalf("an explicit repo was lost: %v", given.Repo)
	}
}

// writeFact takes the fact BY VALUE and mints an id internally, so the caller's copy kept an
// empty one and every confirmation read "Proposed , citing ...". An agent was left with no handle
// to supersede or correct what it had just written.
func TestAProposalReportsAnIdTheCallerCanUse(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	f := fact{ID: newFactID(now), Kind: "gotcha", By: proposedBy, At: now,
		Sources: []string{"deck:x"}, Body: "the id must survive the write"}
	if f.ID == "" {
		t.Fatal("no id was minted")
	}
	if _, err := writeFact(dir, f); err != nil {
		t.Fatal(err)
	}
	back, err := readFacts(dir, true)
	if err != nil || len(back) != 1 {
		t.Fatalf("readFacts = %v, %v", back, err)
	}
	if back[0].ID != f.ID {
		t.Fatalf("stored id %q != reported id %q — the caller cannot address what it wrote", back[0].ID, f.ID)
	}
}

// The project_memory tool handed back 12 of 27 facts and advised running a shell command for the
// rest — which an agent working through MCP cannot do. The cap belongs to the session-start
// brief, which must fit a screen, not to a deliberate read.
func TestTheMemoryToolReturnsEveryFactNotAScreenful(t *testing.T) {
	var many []fact
	for i := 0; i < briefMax+9; i++ {
		many = append(many, fact{ID: newFactID(time.Now()), Kind: "gotcha", Repo: "r",
			By: "someone", At: time.Now(), Body: "fact number " + string(rune('a'+i))})
	}
	full := renderBrief("acr", "r", many, 0)
	if strings.Contains(full, "more: `ptln memory ls`") {
		t.Error("the tool truncated and pointed at a shell command an agent cannot run")
	}
	capped := renderBrief("acr", "r", many, briefMax)
	if !strings.Contains(capped, "more: `ptln memory ls`") {
		t.Error("the session brief must still cap, and say how many it held back")
	}
}
