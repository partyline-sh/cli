package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// board_provider_live_test.go — the provider path end to end, against a REAL subprocess speaking
// real JSON-RPC over stdio.
//
// The unit tests above cover the decode and the sanitizing in isolation; this one covers the parts
// only a live process exercises: the initialize handshake, reading a resource, the scope query
// string, and a provider that misbehaves. It is where a wrong assumption about the wire actually
// shows up.

// writeFakeProvider drops a small MCP server on disk and returns a source pointed at it.
func writeFakeProvider(t *testing.T) providerSource {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available — the live provider test needs a subprocess to talk to")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake_provider.py")
	if err := os.WriteFile(path, []byte(fakeProviderPy), 0o755); err != nil {
		t.Fatal(err)
	}
	return providerSource{name: "odoo", command: "python3", args: []string{path}}
}

func TestLiveProviderListsScopes(t *testing.T) {
	p := writeFakeProvider(t)
	scopes, err := p.Scopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 {
		t.Fatalf("got %d scopes, want 2", len(scopes))
	}
	if scopes[0].Label != "ACR POS" || scopes[0].ID != "42" {
		t.Fatalf("first scope = %+v", scopes[0])
	}
}

func TestLiveProviderLoadsABoard(t *testing.T) {
	p := writeFakeProvider(t)
	d, err := p.Load("42")
	if err != nil {
		t.Fatal(err)
	}

	if d.Source != "odoo" || d.Scope != "ACR POS" {
		t.Fatalf("source/scope = %q/%q", d.Source, d.Scope)
	}
	if d.Live {
		t.Fatal("a provider board must never be live")
	}
	if d.ReadAt.Year() != 2026 {
		t.Fatalf("the provider's own read_at was ignored: %v", d.ReadAt)
	}

	if got := len(d.Columns); got != 3 {
		t.Fatalf("columns = %d, want the 3 the provider declared", got)
	}
	if d.Title("doing") != "In Progress" {
		t.Fatalf("column title = %q", d.Title("doing"))
	}

	card, ok := d.Find("t1")
	if !ok {
		t.Fatal("card t1 missing")
	}
	if !card.Foreign {
		t.Fatal("a provider card must be marked foreign")
	}
	if card.Task != "Receipt printer drops the last line" {
		t.Fatalf("title = %q", card.Task)
	}
	if card.SourceURL != "https://odoo.example/web#id=118" {
		t.Fatalf("url = %q", card.SourceURL)
	}
	if _, urgent := cardState(card); !urgent {
		t.Fatal("the provider declared this one urgent")
	}
}

// The two hostile cases the fake provider deliberately includes.
func TestLiveProviderPayloadIsSanitized(t *testing.T) {
	p := writeFakeProvider(t)
	d, err := p.Load("42")
	if err != nil {
		t.Fatal(err)
	}

	c, ok := d.Find("t2")
	if !ok {
		t.Fatal("card t2 missing")
	}
	if strings.ContainsRune(c.Task, 0x1b) {
		t.Fatalf("an escape sequence in a ticket title reached the board: %q", c.Task)
	}
	if c.SourceURL != "" {
		t.Fatalf("a javascript: URL survived: %q", c.SourceURL)
	}

	if _, ok := d.Find("t3"); ok {
		t.Fatal("a card in a column the provider never declared must be dropped")
	}
}

// A scope actually reaches the provider, which is what makes "show me only this project" work.
func TestLiveProviderReceivesTheScope(t *testing.T) {
	p := writeFakeProvider(t)
	d, err := p.Load("51")
	if err != nil {
		t.Fatal(err)
	}
	if d.Scope != "Odoo Core" {
		t.Fatalf("scope = %q — the provider did not receive the selection", d.Scope)
	}
}

// A provider that is not there, or that answers nonsense, must fail with something readable rather
// than hanging or panicking. The board keeps the previous data and says so.
func TestLiveProviderFailuresAreReadable(t *testing.T) {
	missing := providerSource{name: "gone", command: "definitely-not-a-real-binary-xyz"}
	if _, err := missing.Load(""); err == nil {
		t.Fatal("a missing provider binary must be an error")
	}

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.py")
	// Answers initialize, then returns a resource whose text is not a board.
	bad := "import sys,json\n" +
		"for line in sys.stdin:\n" +
		"    r=json.loads(line)\n" +
		"    if r.get('method')=='initialize':\n" +
		"        print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':{}}),flush=True)\n" +
		"    else:\n" +
		"        print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':{'contents':[{'text':'not a board'}]}}),flush=True)\n"
	if err := os.WriteFile(path, []byte(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	p := providerSource{name: "bad", command: "python3", args: []string{path}}
	_, err := p.Load("")
	if err == nil {
		t.Fatal("a provider returning junk must be an error, not a blank board")
	}
	if !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("error should say the board could not read it, got %q", err)
	}
}

// discoverBoardProviders only picks up servers that OPT IN, so partyline never spawns somebody's
// unrelated MCP server just to ask whether it happens to be a board.
func TestDiscoveryRequiresTheBoardOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	cat := `{"servers":{
	  "odoo":  {"command":"python3","args":["x.py"],"board":true},
	  "other": {"command":"python3","args":["y.py"]}
	}}`
	if err := os.WriteFile(filepath.Join(home, ".partyline", "mcp.json"), []byte(cat), 0o600); err != nil {
		t.Fatal(err)
	}

	got := discoverBoardProviders()
	if len(got) != 1 {
		t.Fatalf("discovered %d providers, want only the one that opted in", len(got))
	}
	if got[0].Name() != "odoo" {
		t.Fatalf("discovered %q", got[0].Name())
	}
}

// The fake provider. Inline so the test is self-contained and cannot drift from a fixture file.
const fakeProviderPy = `import sys, json
def send(o): sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
SCOPES = {"scopes":[{"id":"42","label":"ACR POS","note":"48 open"},
                    {"id":"51","label":"Odoo Core","note":"7 open"}]}
def board(scope):
    name = {"42":"ACR POS","51":"Odoo Core"}.get(scope,"All")
    return {"scope": name,
            "read_at": "2026-09-01T14:02:11Z",
            "columns":[{"key":"new","title":"New"},
                       {"key":"doing","title":"In Progress"},
                       {"key":"done","title":"Done"}],
            "cards":[{"id":"t1","column":"new","title":"Receipt printer drops the last line",
                      "subtitle":name,"detail":"reported by a store","state":"open",
                      "url":"https://odoo.example/web#id=118","urgent":True},
                     {"id":"t2","column":"doing","title":"\x1b[2JTax rounding on refunds",
                      "subtitle":name,"state":"assigned","url":"javascript:alert(1)"},
                     {"id":"t3","column":"ghost","title":"card in an undeclared column"}]}
for line in sys.stdin:
    try: req = json.loads(line)
    except Exception: continue
    m, i = req.get("method"), req.get("id")
    if m == "initialize":
        send({"jsonrpc":"2.0","id":i,"result":{"protocolVersion":"2024-11-05","capabilities":{}}})
    elif m == "resources/read":
        uri = req.get("params",{}).get("uri","")
        if uri.startswith("partyline://board/scopes"): payload = SCOPES
        elif uri.startswith("partyline://board"):
            scope = uri.split("scope=")[1] if "scope=" in uri else ""
            payload = board(scope)
        else:
            send({"jsonrpc":"2.0","id":i,"error":{"code":-32602,"message":"no such resource"}}); continue
        send({"jsonrpc":"2.0","id":i,"result":{"contents":[{"uri":uri,"mimeType":"application/json",
              "text":json.dumps(payload)}]}})
    elif i is not None:
        send({"jsonrpc":"2.0","id":i,"error":{"code":-32601,"message":"unknown"}})
`

// writeFakeProviderWithDetail is the reference provider plus the detail-pane half of the contract.
func writeFakeProviderWithDetail(t *testing.T) providerSource {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	path := filepath.Join(t.TempDir(), "detail_provider.py")
	if err := os.WriteFile(path, []byte(detailProviderPy), 0o755); err != nil {
		t.Fatal(err)
	}
	return providerSource{name: "odoo", command: "python3", args: []string{path}}
}

const detailProviderPy = `import sys, json
def send(o): sys.stdout.write(json.dumps(o)+"\n"); sys.stdout.flush()
BOARD = {"scope":"ACR POS",
  "columns":[{"key":"new","title":"New"}],
  "cards":[{"id":"t1","column":"new","title":"Receipt printer drops a line","state":"open",
            "fields":[{"label":"assignee","value":"Sam"},
                      {"label":"customer","value":"Northgate Store"}],
            "body":"Till 3 only.\n\n\x1b[2JReproduced twice on Friday."}]}
for line in sys.stdin:
    try: req = json.loads(line)
    except Exception: continue
    m, i = req.get("method"), req.get("id")
    if m == "initialize":
        send({"jsonrpc":"2.0","id":i,"result":{"protocolVersion":"2024-11-05","capabilities":{}}})
    elif m == "resources/read":
        uri = req.get("params",{}).get("uri","")
        send({"jsonrpc":"2.0","id":i,"result":{"contents":[{"uri":uri,"mimeType":"application/json",
              "text":json.dumps(BOARD)}]}})
    elif i is not None:
        send({"jsonrpc":"2.0","id":i,"error":{"code":-32601,"message":"unknown"}})
`
