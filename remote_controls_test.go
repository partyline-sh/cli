package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	"partyline.sh/partyline/internal/remote"
)

func TestRemoteLoopControls(t *testing.T) {
	tmuxTestServer(t, "ptln-test-phone-loop-controls")
	store, phone := remoteInputFixture(t)
	now := time.Now()
	seq := 0
	send := func(kind, loop string, data any) remote.Message {
		seq++
		return store.handle(phone, remote.Message{Type: kind, ID: fmt.Sprint(seq), Pane: loop, Data: data}, now.Add(time.Duration(seq)*time.Second))
	}
	prepare := func(r *loopRun, action string) map[string]any {
		t.Helper()
		result := send("loop_prepare", r.ID, map[string]any{"action": action})
		if result.Status != "ok" {
			t.Fatalf("prepare: %+v", result)
		}
		return result.Data.(map[string]any)
	}
	command := func(ch map[string]any) map[string]any {
		return map[string]any{"action": ch["action"], "revision": ch["revision"], "token": ch["token"], "code": ch["code"], "note": "needs smaller scope"}
	}
	t.Run("missing challenge", func(t *testing.T) {
		r := waitingRun(t, loopAwaiting)
		got := send("loop_action", r.ID, map[string]any{"action": "approve", "revision": 1, "code": "0000"})
		if got.Status != "refused" || !strings.Contains(got.Reason, "no live") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("stale revision and changed brief", func(t *testing.T) {
		for _, revision := range []bool{false, true} {
			r := waitingRun(t, loopAwaiting)
			ch := prepare(r, "reject")
			if revision {
				r.Revision++
				_ = r.set(loopAwaiting, "new brief", "brief_revised", nil)
			} else {
				_ = os.WriteFile(loopFile(r.ID, "brief-1.md"), []byte(goodBrief+"\nchanged"), 0600)
			}
			got := send("loop_action", r.ID, command(ch))
			if got.Status != "refused" || !strings.Contains(got.Reason, "changed") {
				t.Fatalf("%+v", got)
			}
		}
	})
	t.Run("binds action phone and expiry", func(t *testing.T) {
		r := waitingRun(t, loopAwaiting)
		ch := prepare(r, "reject")
		cmd := command(ch)
		cmd["action"] = "approve"
		if got := send("loop_action", r.ID, cmd); got.Status != "refused" {
			t.Fatal(got)
		}
		token := ch["token"].(string)
		original := store.challenges[token]
		other := original
		other.Phone = "different"
		store.challenges[token] = other
		if got := send("loop_action", r.ID, command(ch)); got.Status != "refused" {
			t.Fatal(got)
		}
		other = original
		other.Until = now.Add(-time.Second)
		store.challenges[token] = other
		if got := send("loop_action", r.ID, command(ch)); got.Status != "refused" {
			t.Fatal(got)
		}
	})
	t.Run("reject audited once across retry", func(t *testing.T) {
		r := waitingRun(t, loopAwaiting)
		ch := prepare(r, "reject")
		m := remote.Message{Type: "loop_action", ID: "deduplicated-decision", Pane: r.ID, Data: command(ch)}
		first := store.handle(phone, m, now)
		if first.Status != "ok" {
			t.Fatal(first)
		}
		if again := store.handle(phone, m, now.Add(time.Second)); again.Status != "ok" {
			t.Fatal(again)
		}
		got, _ := loadLoopRun(r.ID)
		if got.State != loopRevising {
			t.Fatal(got.State)
		}
		events, _ := loopEvents(r.ID)
		n := 0
		for _, event := range events {
			if event.Type == "rejected" {
				n++
				by := event.Data["by"].(map[string]any)
				if by["phoneId"] != phone || by["confirmed_by"] != "paired_phone_code" {
					t.Fatal(by)
				}
			}
		}
		if n != 1 {
			t.Fatalf("%d decisions", n)
		}
		audit, _ := os.ReadFile(filepath.Join(relayDir(), "events.jsonl"))
		if strings.Count(string(audit), ch["token"].(string)) != 1 || !strings.Contains(string(audit), `"choice":"reject"`) {
			t.Fatal(string(audit))
		}
		if retry := send("loop_action", r.ID, command(ch)); retry.Status != "refused" {
			t.Fatal(retry)
		}
	})
	t.Run("wrong codes lock the gate", func(t *testing.T) {
		r := waitingRun(t, loopAwaiting)
		ch := prepare(r, "reject")
		cmd := command(ch)
		cmd["code"] = "wrong"
		for n := 0; n < gateMaxMisses; n++ {
			if send("loop_action", r.ID, cmd).Status != "refused" {
				t.Fatal("wrong code accepted")
			}
		}
		if got := send("loop_action", r.ID, command(ch)); got.Status != "refused" || !strings.Contains(got.Reason, "wrong codes") {
			t.Fatal(got)
		}
	})
	t.Run("revoked phone cannot prepare or decide", func(t *testing.T) {
		r := waitingRun(t, loopAwaiting)
		ch := prepare(r, "reject")
		path := filepath.Join(relayDir(), "phones.json")
		b, _ := os.ReadFile(path)
		_ = os.WriteFile(path, []byte("[]"), 0600)
		defer os.WriteFile(path, b, 0600)
		for _, kind := range []string{"loop_prepare", "loop_action", "loop_detail", "shell_open"} {
			if got := send(kind, r.ID, command(ch)); got.Status != "refused" || !strings.Contains(got.Reason, "revoked") {
				t.Fatal(got)
			}
		}
	})
	t.Run("dispute changes invalidate code", func(t *testing.T) {
		r := waitingRun(t, loopAwaitYou)
		writeLoopJSON(r.ID, "dispute.json", loopTracked{ID: "F1-1"})
		ch := prepare(r, "dismiss")
		writeLoopJSON(r.ID, "dispute.json", loopTracked{ID: "F1-2"})
		if got := send("loop_action", r.ID, command(ch)); got.Status != "refused" {
			t.Fatal(got)
		}
		ch = prepare(r, "dismiss")
		if got := send("loop_action", r.ID, command(ch)); got.Status != "ok" {
			t.Fatal(got)
		}
		var decided []loopTracked
		readLoopJSON(r.ID, "decided.json", &decided)
		if len(decided) != 1 || decided[0].ID != "F1-2" || decided[0].Decision != "dismiss" {
			t.Fatal(decided)
		}
	})
	t.Run("full detail includes brief and lower priority findings", func(t *testing.T) {
		r := waitingRun(t, loopBuilding)
		r.Round = 1
		_ = r.set(loopBuilding, "checking", "test", nil)
		writeLoopJSON(r.ID, "findings-1.json", []loopTracked{{ID: "F1-1", loopFinding: loopFinding{Priority: "P2", Claim: "Handle empty imports"}}})
		writeLoopJSON(r.ID, "checks-1.json", loopCheckRound{Checks: []loopCheck{{Cmd: "make test", Exit: 1, Tail: "actual check failure"}}})
		result := send("loop_detail", r.ID, nil)
		b, _ := json.Marshal(result.Data)
		if result.Status != "ok" || !strings.Contains(string(b), "Handle empty imports") || !strings.Contains(string(b), "actual check failure") || !strings.Contains(string(b), "Export tickets as CSV") {
			t.Fatal(string(b))
		}
	})
	t.Run("read results do not accumulate and oversized briefs are refused intact", func(t *testing.T) {
		r := waitingRun(t, loopAwaiting)
		seq++
		id := fmt.Sprint(seq)
		result := store.handle(phone, remote.Message{Type: "loop_detail", ID: id, Pane: r.ID}, now)
		if result.Status != "ok" {
			t.Fatal(result)
		}
		if _, cached := store.phones[phone].results[id]; cached {
			t.Fatal("large read response cached")
		}
		_ = os.WriteFile(loopFile(r.ID, "brief-1.md"), []byte(strings.Repeat("long brief", 60000)), 0600)
		if got := send("loop_detail", r.ID, nil); got.Status != "refused" || !strings.Contains(got.Reason, "512 KiB") {
			t.Fatal(got)
		}
		if got := send("loop_prepare", r.ID, map[string]any{"action": "approve"}); got.Status != "refused" {
			t.Fatal(got)
		}
	})

	t.Run("audit failure prevents a decision", func(t *testing.T) {
		r := waitingRun(t, loopAwaiting)
		ch := prepare(r, "reject")
		path := filepath.Join(relayDir(), "events.jsonl")
		backup := path + ".backup"
		if err := os.Rename(path, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(path); _ = os.Rename(backup, path) }()
		if got := send("loop_action", r.ID, command(ch)); got.Status != "refused" {
			t.Fatal(got)
		}
		current, _ := loadLoopRun(r.ID)
		if current.State != loopAwaiting {
			t.Fatal("unaudited decision changed the loop")
		}
	})
}

func TestRemoteShellIsAnOrdinaryAuthorizedSession(t *testing.T) {
	tmuxTestServer(t, "ptln-test-phone-shell")
	store, phone := remoteInputFixture(t)
	now := time.Now()
	m := remote.Message{Type: "shell_open", ID: "one-shell"}
	result := store.handle(phone, m, now)
	if result.Status != "ok" {
		t.Fatal(result)
	}
	pane := result.Data.(map[string]string)["pane"]
	if !strings.HasPrefix(pane, "%") {
		t.Fatal(pane)
	}
	inventory, _ := remoteSnapshot("sessions", "focus")
	if rows := inventory.([]map[string]string); len(rows) != 1 || rows[0]["pane"] != pane || rows[0]["engine"] != "" {
		t.Fatal(rows)
	}
	if again := store.handle(phone, m, now); again.Data.(map[string]string)["pane"] != pane {
		t.Fatal(again)
	}
	second := store.handle(phone, remote.Message{Type: "shell_open", ID: "second-shell"}, now)
	if second.Status != "ok" || second.Data.(map[string]string)["pane"] == pane {
		t.Fatal(second)
	}
	got := store.handle(phone, remote.Message{Type: "input", ID: "shell-input", Pane: pane, Kind: "text", Data: "printf 'phone-shell-%s\\n' works"}, now)
	if got.Status != "ok" {
		t.Fatal(got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, _ := remoteSnapshot(pane, "focus")
		if strings.Contains(out.(string), "phone-shell-works") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := store.handle(phone, remote.Message{Type: "shell_open", ID: "injected-shell", Data: "rm -rf anything"}, now); got.Status != "refused" {
		t.Fatal(got)
	}
}

func TestRemoteLoopApprovalAndBatchControls(t *testing.T) {
	tmuxTestServer(t, "ptln-test-phone-batch-controls")
	repo, b, r := approvedBatch(t)
	store, phone := remoteInputFixture(t)
	now := time.Now()
	sequence := 0
	send := func(kind, id string, data any) remote.Message {
		sequence++
		return store.handle(phone, remote.Message{Type: kind, ID: fmt.Sprint(sequence), Pane: id, Data: data}, now.Add(time.Duration(sequence)*time.Second))
	}
	act := func(id, action string) remote.Message {
		t.Helper()
		prepared := send("loop_prepare", id, map[string]any{"action": action})
		if prepared.Status != "ok" {
			t.Fatal(prepared)
		}
		ch := prepared.Data.(map[string]any)
		return send("loop_action", id, map[string]any{"action": action, "token": ch["token"], "code": ch["code"], "revision": ch["revision"]})
	}
	plan := fmt.Sprintf("plan-%s-2", b.ID)
	detail := send("loop_detail", plan, nil)
	if detail.Status != "ok" || detail.Data.(map[string]any)["brief"] != b.Briefs[1] {
		t.Fatal(detail)
	}
	if got := send("loop_prepare", plan, map[string]any{"action": "approve"}); got.Status != "refused" {
		t.Fatal(got)
	}
	// A phone can overrule precisely the standing arbiter dismissal it read.
	if err := withBatch(b.ID, func(b *loopBatch) error {
		b.Arbitrated = []loopArbitration{{Step: 1, Finding: loopTracked{ID: "F1-1"}, Decision: "dismiss", Confidence: 0.9}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := act(r.ID, "reverse"); got.Status != "ok" {
		t.Fatal(got)
	}
	b, _ = loadBatch(b.ID)
	if !b.Arbitrated[0].Reversed || len(b.Deferred) != 1 || b.Deferred[0].ID != "S1-F1-1" {
		t.Fatal(b)
	}
	// A challenge for one planned card cannot be submitted through another.
	prepared := send("loop_prepare", plan, map[string]any{"action": "delete"})
	if prepared.Status != "ok" {
		t.Fatal(prepared)
	}
	ch := prepared.Data.(map[string]any)
	if got := send("loop_action", fmt.Sprintf("plan-%s-3", b.ID), map[string]any{"action": "delete", "token": ch["token"], "code": ch["code"], "revision": ch["revision"]}); got.Status != "refused" {
		t.Fatal(got)
	}
	// Delete from a planned step names/removes the entire batch, not a fake run.
	if got := act(plan, "delete"); got.Status != "ok" {
		t.Fatal(got)
	}
	if _, err := loadBatch(b.ID); err == nil {
		t.Fatal("batch still exists")
	}
	if _, err := loadLoopRun(r.ID); err == nil {
		t.Fatal("step still exists")
	}
	// Approval keeps the existing config checks and baseline dispatch.
	r = waitingRun(t, loopAwaiting)
	r.Repo = repo
	_ = r.set(loopAwaiting, "ready", "test", nil)
	if got := act(r.ID, "approve"); got.Status != "ok" {
		t.Fatal(got)
	}
	r, _ = loadLoopRun(r.ID)
	if r.State != loopBaseline {
		t.Fatal(r.State)
	}
}

func TestRemoteLoopDecisionRequiresEncryptedPhoneDispatch(t *testing.T) {
	tmuxTestServer(t, "ptln-test-phone-control-wire")
	store, route := remoteInputFixture(t)
	r := waitingRun(t, loopAwaiting)
	machine, err := pairStatic()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]remotePhone{{ID: route, Static: phone.Public}})
	_ = os.WriteFile(filepath.Join(relayDir(), "phones.json"), b, 0600)
	channel := newRemoteChannel()
	channel.inputs = store
	now := time.Now()
	request := remote.Message{Type: "loop_prepare", ID: "wire-prepare", Pane: r.ID, Data: map[string]any{"action": "reject"}}
	raw, _ := json.Marshal([]remote.Message{request})
	if err := channel.receive(route, raw, now); err == nil {
		t.Fatal("plaintext command accepted")
	}
	for attempt := 0; attempt < 2; attempt++ {
		channel.drop(route)
		h, err := remote.NewNoise(remote.NoiseConfig{Pattern: "KK", Initiator: true, Static: phone, PeerStatic: machine.Public})
		if err != nil {
			t.Fatal(err)
		}
		hello, _, _, _ := h.WriteMessage(nil, nil)
		if err := channel.receive(route, hello, now); err != nil {
			t.Fatal(err)
		}
		_, send, receive, err := h.ReadMessage(nil, channel.pending[route])
		if err != nil {
			t.Fatal(err)
		}
		peer := channel.peers[route]
		call := func(m remote.Message) remote.Message {
			t.Helper()
			raw, _ := json.Marshal([]remote.Message{m})
			wire, _ := send.Encrypt(nil, nil, raw)
			if err := channel.receive(route, wire, now); err != nil {
				t.Fatal(err)
			}
			wire, err = peer.Flush()
			if err != nil {
				t.Fatal(err)
			}
			raw, err = receive.Decrypt(nil, nil, wire)
			if err != nil {
				t.Fatal(err)
			}
			var messages []remote.Message
			if err := json.Unmarshal(raw, &messages); err != nil {
				t.Fatal(err)
			}
			for _, result := range messages {
				if result.Type == "input_result" && result.ID == m.ID {
					return result
				}
			}
			t.Fatalf("no reply: %s", raw)
			return remote.Message{}
		}
		if attempt == 0 {
			prepared := call(request)
			if prepared.Status != "ok" {
				t.Fatal(prepared)
			}
			ch := prepared.Data.(map[string]any)
			request = remote.Message{Type: "loop_action", ID: "wire-decision", Pane: r.ID, Data: map[string]any{"action": "reject", "note": "scope", "revision": ch["revision"], "token": ch["token"], "code": ch["code"]}}
		}
		if got := call(request); got.Status != "ok" {
			t.Fatal(got)
		}
	}
	events, _ := loopEvents(r.ID)
	decisions := 0
	for _, event := range events {
		if event.Type == "rejected" {
			decisions++
		}
	}
	if decisions != 1 {
		t.Fatalf("reconnect caused %d decisions", decisions)
	}
}
