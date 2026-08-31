package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/api"
	"partyline.sh/partyline/internal/surfacegen"
)

// promptResult is the shape of a prompts/get reply, decoded far enough to read the one message.
type promptResult struct {
	Result struct {
		Description string `json:"description"`
		Messages    []struct {
			Role    string `json:"role"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	} `json:"result"`
	Error *rpcError `json:"error"`
}

// rpc drives the server through its real JSON-RPC entry point, so the test exercises the same path
// an engine does rather than calling the handler directly.
func rpc(t *testing.T, s *cgServer, method string, params map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	s.handle(raw, json.NewEncoder(&out))
	return out.String()
}

// The reference must be OFFERED (or a session never learns it exists) and must actually return the
// document. Both halves matter: a listed prompt that returns nothing is worse than no prompt.
func TestReferencePromptIsListedAndReturnsTheDocument(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no token, no bind — nothing here may need either
	s := &cgServer{c: api.New()}

	if listed := rpc(t, s, "prompts/list", nil); !strings.Contains(listed, partylineReferencePromptName) {
		t.Fatalf("%s is not listed:\n%s", partylineReferencePromptName, listed)
	}

	var got promptResult
	if err := json.Unmarshal([]byte(rpc(t, s, "prompts/get", map[string]any{"name": partylineReferencePromptName})), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != nil {
		t.Fatalf("prompts/get errored: %+v", got.Error)
	}
	if len(got.Result.Messages) != 1 {
		t.Fatalf("want exactly one message, got %d", len(got.Result.Messages))
	}
	if strings.TrimSpace(got.Result.Messages[0].Content.Text) == "" {
		t.Fatal("the reference prompt returned empty content")
	}
}

// The guarantee this prompt exists to make: it hands out the bytes of the GENERATED artifact that
// the web serves at partyline.sh/llms-full.txt. Checked against BOTH the file on disk (what the URL
// actually serves) and a fresh generation from the source tree (so a stale checked-in file cannot
// make this pass quietly). Anything but equality means the two surfaces have started to disagree.
func TestReferencePromptIsByteIdenticalToTheServedArtifact(t *testing.T) {
	const artifact = "web/public/llms-full.txt"

	onDisk, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("the artifact the URL serves is missing: %v", err)
	}

	files, err := surfacegen.Files(".")
	if err != nil {
		t.Fatalf("generating the surface artifacts: %v", err)
	}
	var generated []byte
	for _, f := range files {
		if f.Path == artifact {
			generated = f.Body
		}
	}
	if generated == nil {
		t.Fatalf("%s is no longer a generated artifact — this prompt's source moved", artifact)
	}
	if !bytes.Equal(onDisk, generated) {
		t.Fatalf("%s on disk is stale vs the generator — run `make surface-gen`", artifact)
	}

	t.Setenv("HOME", t.TempDir())
	s := &cgServer{c: api.New()}
	var got promptResult
	if err := json.Unmarshal([]byte(rpc(t, s, "prompts/get", map[string]any{"name": partylineReferencePromptName})), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != nil {
		t.Fatalf("prompts/get errored: %+v", got.Error)
	}
	if served := got.Result.Messages[0].Content.Text; served != string(generated) {
		t.Fatalf("the prompt does not serve the generated document byte-for-byte (%d bytes vs %d)",
			len(served), len(generated))
	}
}

// Understanding partyline is not a setup step: the reference must answer identically with no thread
// bound, and must never degrade into the "this repo has no shared context" text every other prompt
// falls back to.
func TestReferencePromptWorksWithNoThreadBound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir()) // not a git repo either — nothing to resolve a thread from

	s := &cgServer{c: api.New()}
	var got promptResult
	if err := json.Unmarshal([]byte(rpc(t, s, "prompts/get", map[string]any{"name": partylineReferencePromptName})), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != nil {
		t.Fatalf("prompts/get errored with no thread: %+v", got.Error)
	}
	text := got.Result.Messages[0].Content.Text
	if text != partylineReferenceDoc {
		t.Fatal("with no thread bound the prompt did not serve the document")
	}
	if strings.Contains(text, cgNoThread) {
		t.Fatal("the reference degraded into the no-thread message")
	}
	if s.thread != "" {
		t.Fatalf("the reference resolved a thread it does not need: %q", s.thread)
	}
}

// The briefing injected into EVERY session's system prompt must stay small — that is the whole
// reason the full document is a fetched-on-demand prompt instead. This is a ratchet: if the
// briefing ever grows toward the size of the reference, the split has been undone.
func TestInitializeBriefingStaysSmall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	brief := cgInstructions("fa365970-def0-4321-a8f1-630a723ef35c")
	const budget = 4096
	if len(brief) > budget {
		t.Fatalf("the initialize briefing is %d bytes, over its %d-byte budget — it is injected into "+
			"every session's system prompt; put new material in the %s prompt instead",
			len(brief), budget, partylineReferencePromptName)
	}
	if strings.Contains(brief, partylineReferenceDoc) {
		t.Fatal("the full reference has been inlined into the initialize briefing")
	}
}
