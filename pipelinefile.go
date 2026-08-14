// G.6 — reading the project's pipeline policy off disk.
//
// The daemon writes this file from the run event; crank reads it here. That makes it a control-plane
// value arriving on the machine that executes things, which is exactly the boundary the whole gate
// design is built around — so this file's job is to be paranoid about a file we ourselves wrote.
//
// The rule it enforces: POLICY IN, NEVER A COMMAND. A check is addressed by NAME and the name has to
// match the repo's own; a lane names an ENGINE from the closed set and a MODEL of a safe shape. There
// is no field here that becomes a path, an argv, or a shell string, and applyPolicy will drop any
// name the repo does not declare regardless of what this file says.
//
// FAIL-SOFT, DELIBERATELY. Every failure — missing file, bad JSON, an invalid entry — degrades to the
// DEFAULT pipeline (all repo checks blocking and always-run, one reviewer). That is the strict
// direction: a corrupt policy file makes the gate stricter and noisier, never quieter. The one thing
// it must never do is let a malformed file weaken or widen the gate.
package main

import (
	"encoding/json"
	"os"
	"strings"

	eng "partyline.sh/partyline/internal/engine"
)

const (
	maxPipelineBytes = 64 << 10 // a policy document, not a payload
	maxPolicyChecks  = 100
	maxPolicyLanes   = 4 // each lane is a full reviewer pass; four is already 4x tokens
)

// pipelineFile is the on-disk shape. Named fields rather than a bare map so an unexpected key is
// ignored instead of reaching anything.
type pipelineFile struct {
	Checks []checkPolicy `json:"checks"`
	Lanes  []lanePolicy  `json:"lanes"`
}

// lanePolicy is a reviewer lane as the control plane describes it: who to ask, not what to run.
type lanePolicy struct {
	ID     string `json:"id"`
	Engine string `json:"engine"`
	Model  string `json:"model,omitempty"`
}

// readPipelineFile parses and VALIDATES the policy file. Anything that does not survive validation
// is dropped, and a wholly unusable file yields the default pipeline.
func readPipelineFile(path string) ([]checkPolicy, []reviewLane) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || len(b) > maxPipelineBytes {
		return nil, nil
	}
	var f pipelineFile
	if json.Unmarshal(b, &f) != nil {
		return nil, nil
	}
	return validChecks(f.Checks), validLanes(f.Lanes)
}

// validChecks keeps only entries whose name could actually key a repo-declared check. A name that
// fails checkNameRe could never match one, so keeping it would only be a way to smuggle a long
// string into memory.
func validChecks(in []checkPolicy) []checkPolicy {
	var out []checkPolicy
	seen := map[string]bool{}
	for _, c := range in {
		name := strings.TrimSpace(c.Name)
		if !checkNameRe.MatchString(name) || seen[name] {
			continue
		}
		// A glob is matched against paths, never expanded by a shell — but bound it anyway, at the
		// same 200 the column allows, so the two ends agree.
		glob := strings.TrimSpace(c.PathGlob)
		if len(glob) > 200 {
			continue
		}
		seen[name] = true
		out = append(out, checkPolicy{Name: name, Enabled: c.Enabled, Blocking: c.Blocking, PathGlob: glob})
		if len(out) >= maxPolicyChecks {
			break
		}
	}
	return out
}

// validLanes keeps only lanes this machine can actually spawn. eng.Valid is the same closed set the
// daemon uses everywhere else: a server value never becomes an argv without being re-checked on the
// machine that will run it, because the server does not know what is installed here.
func validLanes(in []lanePolicy) []reviewLane {
	var out []reviewLane
	seen := map[string]bool{}
	for _, l := range in {
		id, engine, model := strings.TrimSpace(l.ID), strings.TrimSpace(l.Engine), strings.TrimSpace(l.Model)
		if !checkNameRe.MatchString(id) || seen[id] || !eng.Valid(engine) {
			continue
		}
		// An empty model means "the engine's own default" and is the common case. A non-empty one
		// heads for an exec argv, so it passes the same shape gate as every other model token.
		if model != "" && !modelRe.MatchString(model) {
			continue
		}
		seen[id] = true
		out = append(out, reviewLane{ID: id, Engine: engine, Model: model})
		if len(out) >= maxPolicyLanes {
			break
		}
	}
	return out
}
