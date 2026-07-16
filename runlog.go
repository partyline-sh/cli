// Tamper-evident run log (TRUST · T1). crank already self-reports each task's lifecycle to the
// run store (#263: the run_tasks projection). On top of that, the daemon keeps an APPEND-ONLY,
// hash-chained ledger of the same transitions in run_events — so a run's history is auditable and
// tamper-evident: every event links to the previous by hash, and any insert/reorder/delete/edit
// breaks the chain on re-verify.
//
// The chain is per (run, daemon): this crank process is one daemon working one run, so its event
// order is well-defined (it's a single loop). seq is 0-based and monotonic; hash binds the event's
// position (seq), kind, task_idx, and payload to the prior hash. The DAEMON computes the chain (it's
// the authority on its own execution); the server enforces continuity on append and derives
// daemon_id from the device token. This file is the producer half; a Go re-verifier (same code,
// byte-identical preimage) is the authoritative full-integrity check.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"partyline.sh/partyline/internal/api"
)

// chainState carries the running head of one daemon's chain for a run. Not safe for concurrent use
// — a single crank process emits sequentially, so it's mutated only from the reporter's post path.
type chainState struct {
	seq      int    // next seq to assign
	lastHash string // hash of the last committed event ("" before the genesis event)
}

// eventHash is the canonical preimage → sha256 hex. Both halves (this producer and the Go
// re-verifier) MUST build the preimage identically: prev_hash + "\n" + compact JSON of a fixed
// shape {seq, kind, task_idx, payload}. Go's json.Marshal sorts map keys, so the payload
// serialization is deterministic; task_idx is a pointer so a run-level event (nil) hashes
// distinctly from idx 0.
func eventHash(prev string, seq int, kind string, taskIdx *int, payload map[string]any) string {
	body, _ := json.Marshal(map[string]any{
		"seq":      seq,
		"kind":     kind,
		"task_idx": taskIdx,
		"payload":  payload,
	})
	sum := sha256.Sum256(append([]byte(prev+"\n"), body...))
	return hex.EncodeToString(sum[:])
}

// build returns the next event for a task update WITHOUT advancing the head — the caller commits
// only after a successful append (see commit). This keeps best-effort telemetry from poisoning the
// chain: a dropped append leaves the head where it was, so the next transition reuses that seq and
// the chain stays continuous (the lost transition is simply absent from the ledger, not a gap). The
// hash is computed over the current head; payload mirrors the run_tasks projection fields (omitting
// empties, exactly like UpsertRunTask) so the ledger and the projection tell the same story.
func (c *chainState) build(tr api.RunTaskUpdate) api.RunLogEvent {
	payload := map[string]any{"status": tr.Status}
	if tr.Task != "" {
		payload["task"] = tr.Task
	}
	if tr.Branch != "" {
		payload["branch"] = tr.Branch
	}
	if tr.Detail != "" {
		payload["detail"] = tr.Detail
	}
	if tr.PRURL != "" {
		payload["pr_url"] = tr.PRURL
	}
	if tr.Summary != "" {
		payload["summary"] = tr.Summary
	}
	if tr.Tokens > 0 {
		payload["tokens"] = tr.Tokens
	}
	if tr.DurationMs > 0 {
		payload["duration_ms"] = tr.DurationMs
	}
	if tr.Verified != "" {
		payload["verified"] = tr.Verified // Trust · T2a: verify verdict, bound into the chain
	}
	idx := tr.Idx
	hash := eventHash(c.lastHash, c.seq, tr.Status, &idx, payload)
	return api.RunLogEvent{
		Seq: c.seq, PrevHash: c.lastHash, Hash: hash,
		Kind: tr.Status, TaskIdx: &idx, Payload: payload,
	}
}

// commit rolls the head forward to a just-appended event. Call ONLY after AppendRunEvent succeeds
// so the in-memory head tracks the server's stored head exactly.
func (c *chainState) commit(ev api.RunLogEvent) {
	c.seq = ev.Seq + 1
	c.lastHash = ev.Hash
}
