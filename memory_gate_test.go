package main

import (
	"strings"
	"testing"
)

// The gate is calibrated against the REAL store, not invented examples. Every string below was
// read out of the live context_blocks table or the local memory directory on 2026-10-17.
//
// The refusals are the four shapes that made a good store feel like junk. The acceptances are the
// facts that made it worth keeping — and they matter more: a gate that blocks those is worse than
// no gate at all.
func TestGateRefusesWhatMadeTheStoreUntrusted(t *testing.T) {
	refuse := []struct{ name, kind, body string }{
		{
			"host disk state",
			"constraint",
			"The Mac mini's 228 GB disk keeps filling up, and the pending macOS Tahoe 26.7.1 update needs 25 GB free.",
		},
		{
			"machine config",
			"constraint",
			"Both the laptop and the Mac mini have Screen Sharing on, with port 5900 listening on every address and a public IPv6 address.",
		},
		{
			"work item status",
			"constraint",
			"Receipt #81 in xero-receipts is staged with the wrong coding (310, a settlement top-up) and must not be pushed to BC's Xero.",
		},
		{
			"too short to stand alone",
			"decision",
			"Use postgres.",
		},
	}
	for _, c := range refuse {
		v := screenFact(c.kind, c.body)
		if !v.Refuse {
			t.Errorf("%s: should have been refused\n  %s", c.name, c.body)
			continue
		}
		if v.Reason == "" {
			t.Errorf("%s: refused with no reason, so the agent cannot fix it", c.name)
		}
	}
}

// These are the facts the system exists to keep. Refusing any of them would be a regression worth
// reverting the gate over.
func TestGateAcceptsTheFactsWorthKeeping(t *testing.T) {
	accept := []struct{ name, kind, body string }{
		{
			"API quirk",
			"gotcha",
			"Xero's /Reports/TrialBalance ignores the date parameter and returns activity to date. Its columns are Account, Debit, Credit, YTD Debit and YTD Credit.",
		},
		{
			"inverted verb semantics",
			"constraint",
			"Xero API behaviour: PUT /Accounts creates an account and POST updates one. Reconciled bank transactions cannot be edited or voided until they are un-reconciled in the UI.",
		},
		{
			"library path hazard mentioning a path",
			"gotcha",
			"Never put /u/acr/obj on a process-wide or global library path. Its vendored OpenSSL and curl shared objects shadow the host's and break unrelated binaries.",
		},
		{
			"architecture, mentions no SQL database",
			"constraint",
			"There is no SQL database on a store server. The POS is 30-year-old C on Linux storing data in ISAM files, with a Java layer above it.",
		},
		{
			"design rule with rationale",
			"decision",
			"Sales rollups are stored by UTC hour, and days, weeks (Monday start), months and years are built in each viewer's own time zone.",
		},
		{
			"load-bearing contract naming a PR",
			"contract",
			"The 64-BIT event stream uses ONE schema wrapped in CloudEvents 1.0 JSON, retained and replayable for 90 days, routed by event type.",
		},
		{
			"a durable growth constraint that mentions disk",
			"constraint",
			"The ISAM files grow without bound and will fill a store server's disk, because nothing in the POS prunes them and there is no retention job.",
		},
	}
	for _, c := range accept {
		if v := screenFact(c.kind, c.body); v.Refuse {
			t.Errorf("%s: wrongly refused (%s)\n  %s", c.name, v.Reason, c.body)
		}
	}
}

// An open question is exempt from the transient shapes. "Is port 5900 reachable over IPv6" is a
// poor constraint and a legitimate question — and refusing to record open questions pushes agents
// into inventing answers, which is the failure the whole system exists to prevent.
func TestGateLetsOpenQuestionsThrough(t *testing.T) {
	q := "Is port 5900 on the Mac mini and laptop reachable from the internet over IPv6? It depends on whether the Telus router drops inbound."
	if v := screenFact("question", q); v.Refuse {
		t.Errorf("an open question was refused (%s)", v.Reason)
	}
	// The same text as a constraint IS refused — it is a machine question, not a repo property.
	if v := screenFact("constraint", q); !v.Refuse {
		t.Error("the same text as a constraint should not pass")
	}
}

// The refusal has to teach, because the caller is usually an agent that will otherwise rephrase
// and retry the same bad fact.
func TestRefusalExplainsTheTest(t *testing.T) {
	v := screenFact("decision", "x")
	err := v.gateError()
	if err == nil {
		t.Fatal("no error for a refused fact")
	}
	for _, want := range []string{"three months", "not a durable fact"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q:\n%s", want, err.Error())
		}
	}
	if screenFact("gotcha", "Xero's TrialBalance ignores the date parameter and returns activity to date.").gateError() != nil {
		t.Error("an accepted fact produced an error")
	}
}
