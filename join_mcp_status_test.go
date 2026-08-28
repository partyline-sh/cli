package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The three promises of `ptln join-mcp status`, as tests: it finds registrations in EVERY config
// file and says which file each came from; it never emits a party token; and a run leaves every
// file it read byte-identical. Plus the exit rule — an ended party fails, an uncheckable one does
// not and is never rendered as ended.

const secretToken = "plt_pty_do-not-print-me"

// writeMachine lays down a fake machine: a home directory with all three engine configs and a
// working directory with a project-local .mcp.json. Server names are deliberately varied and one
// entry is a decoy with no party env — identification is by PARTYLINE_PARTY_ID, never the name.
func writeMachine(t *testing.T, base string) (home, dir string) {
	t.Helper()
	home, dir = t.TempDir(), t.TempDir()
	projectDir := filepath.Join(home, "code", "app")

	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := func(id, name string) string {
		return `{"command":"ptln","args":["party-mcp"],"env":{` +
			`"PARTYLINE_PARTY_BASE":"` + base + `",` +
			`"PARTYLINE_PARTY_ID":"` + id + `",` +
			`"PARTYLINE_PARTY_TOKEN":"` + secretToken + `",` +
			`"PARTYLINE_AGENT_NAME":"` + name + `"}}`
	}

	// claude: the user scope AND the per-directory scope `--scope local` writes.
	write(filepath.Join(home, ".claude.json"), `{"mcpServers":{`+
		`"a-party-by-another-name":`+env("live-user", "you")+`,`+
		`"some-other-server":{"command":"other","env":{"NOT_OURS":"1"}}},`+
		`"projects":{"`+projectDir+`":{"mcpServers":{"partyline-party":`+env("ended-dir", "you")+`}}}}`)

	write(filepath.Join(home, ".gemini", "settings.json"),
		`{"mcpServers":{"partyline-party":`+env("unreachable-gemini", "you")+`}}`)

	// codex speaks TOML in two forms — a dotted key and an [mcp_servers.x.env] table.
	write(filepath.Join(home, ".codex", "config.toml"), `
mcp_servers.dotted.command = "ptln"
mcp_servers.dotted.env.PARTYLINE_PARTY_BASE = "`+base+`"
mcp_servers.dotted.env.PARTYLINE_PARTY_ID = "live-codex-dotted"
mcp_servers.dotted.env.PARTYLINE_PARTY_TOKEN = "`+secretToken+`"

[mcp_servers.tabled]
command = "ptln"
[mcp_servers.tabled.env]
PARTYLINE_PARTY_BASE = "`+base+`"
PARTYLINE_PARTY_ID = "live-codex-tabled"
PARTYLINE_PARTY_TOKEN = "`+secretToken+`"
`)

	write(filepath.Join(dir, ".mcp.json"), `{"mcpServers":{"project-scoped":`+env("live-project", "you")+`}}`)
	return home, dir
}

// partyServer answers the probe's two reads per party id: /info resolves only for a live party,
// /messages resolves for any party that exists at all.
func partyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/parties/"), "/")[0]
		switch {
		case strings.HasPrefix(id, "live"):
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(id, "ended"):
			// Closed: the open-only read refuses, the any-status read still resolves.
			if strings.HasSuffix(r.URL.Path, "/info") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusInternalServerError) // no verdict — uncheckable
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestJoinMCPStatusListsEveryConfigFileAndSaysWhichOneEachCameFrom(t *testing.T) {
	srv := partyServer(t)
	home, dir := writeMachine(t, srv.URL)

	regs := scanPartyRegistrations(home, dir)
	got := map[string]partyRegistration{}
	for _, r := range regs {
		got[r.PartyID] = r
	}
	if len(regs) != 6 {
		t.Fatalf("found %d registrations, want 6: %+v", len(regs), regs)
	}
	for id, wantSource := range map[string]string{
		"live-user":          filepath.Join(home, ".claude.json"),
		"ended-dir":          filepath.Join(home, ".claude.json"),
		"unreachable-gemini": filepath.Join(home, ".gemini", "settings.json"),
		"live-codex-dotted":  filepath.Join(home, ".codex", "config.toml"),
		"live-codex-tabled":  filepath.Join(home, ".codex", "config.toml"),
		"live-project":       filepath.Join(dir, ".mcp.json"),
	} {
		r, ok := got[id]
		if !ok {
			t.Fatalf("party %s was not found at all", id)
		}
		if r.Source != wantSource {
			t.Errorf("party %s attributed to %s, want %s", id, r.Source, wantSource)
		}
	}
	// The per-directory scope is the one a repo-scoped listing would miss.
	if scope := got["ended-dir"].Scope; !strings.Contains(scope, filepath.Join(home, "code", "app")) {
		t.Errorf("per-directory entry does not name its directory: %q", scope)
	}
	if got["live-user"].Scope != "user" {
		t.Errorf("user-scope entry scoped %q, want user", got["live-user"].Scope)
	}
	// Identified by env, not by name: the oddly-named one is in, the decoy is out.
	if got["live-user"].Server != "a-party-by-another-name" {
		t.Errorf("entry identified by name rather than env: %+v", got["live-user"])
	}
	for _, r := range regs {
		if r.Server == "some-other-server" {
			t.Error("a server with no PARTYLINE_PARTY_ID was reported as ours")
		}
	}
}

func TestJoinMCPStatusNeverPrintsAPartyToken(t *testing.T) {
	srv := partyServer(t)
	home, dir := writeMachine(t, srv.URL)
	regs := scanPartyRegistrations(home, dir)
	probePartyRegistrations(regs)

	if regs[0].token == "" {
		t.Fatal("the fixture's token never reached the registration — this test would pass vacuously")
	}
	for _, asJSON := range []bool{false, true} {
		var buf bytes.Buffer
		renderJoinMCPStatus(&buf, regs, asJSON)
		if strings.Contains(buf.String(), secretToken) {
			t.Errorf("the party token appears in the output (json=%v)", asJSON)
		}
	}
	// Structural, not incidental: no exported field holds it, so no future marshal can leak it.
	rt := reflect.TypeOf(partyRegistration{})
	for i := 0; i < rt.NumField(); i++ {
		if f := rt.Field(i); f.IsExported() && strings.Contains(strings.ToLower(f.Name), "token") {
			t.Errorf("partyRegistration.%s is exported — encoding/json can reach the token", f.Name)
		}
	}
}

func TestJoinMCPStatusWritesNothing(t *testing.T) {
	srv := partyServer(t)
	home, dir := writeMachine(t, srv.URL)

	files := map[string][]byte{}
	for _, p := range []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".gemini", "settings.json"),
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(dir, ".mcp.json"),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		files[p] = b
	}

	regs := scanPartyRegistrations(home, dir)
	probePartyRegistrations(regs)
	renderJoinMCPStatus(&bytes.Buffer{}, regs, false)

	for p, before := range files {
		after, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if sha256.Sum256(before) != sha256.Sum256(after) {
			t.Errorf("%s was modified by a read-only command", p)
		}
	}
	// And nothing new appeared beside them either.
	for _, root := range []string{home, dir} {
		var extra []string
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				if _, known := files[p]; !known {
					extra = append(extra, p)
				}
			}
			return nil
		})
		if len(extra) > 0 {
			t.Errorf("files were created: %v", extra)
		}
	}
}

func TestJoinMCPStatusEndedFailsAndUncheckableDoesNot(t *testing.T) {
	srv := partyServer(t)
	home, dir := writeMachine(t, srv.URL)
	regs := scanPartyRegistrations(home, dir)
	probePartyRegistrations(regs)

	byID := map[string]partyRegistration{}
	for _, r := range regs {
		byID[r.PartyID] = r
	}
	for id, want := range map[string]string{
		"live-user":          statusLive,
		"live-codex-tabled":  statusLive,
		"ended-dir":          statusEnded,
		"unreachable-gemini": statusUncheckable,
	} {
		if got := byID[id].Status; got != want {
			t.Errorf("party %s probed %q, want %q", id, got, want)
		}
	}

	var buf bytes.Buffer
	if renderJoinMCPStatus(&buf, regs, false) {
		t.Error("a machine with an ended party reported clean — the command would exit 0")
	}

	// The party we could not check must not be dressed up as ended, in either form.
	unchecked := []partyRegistration{byID["unreachable-gemini"]}
	buf.Reset()
	if !renderJoinMCPStatus(&buf, unchecked, false) {
		t.Error("an uncheckable party failed the run — it is not evidence that anything ended")
	}
	// Not a bare "ended" search — the summary counts and the explanatory line both use the word
	// legitimately. What must not appear is this entry MARKED ended.
	if strings.Contains(buf.String(), "✗") || strings.Contains(buf.String(), "has ended") {
		t.Errorf("an uncheckable party was rendered as ended:\n%s", buf.String())
	}
	buf.Reset()
	renderJoinMCPStatus(&buf, unchecked, true)
	var doc struct {
		Registrations []struct {
			Status string `json:"status"`
		} `json:"registrations"`
		Ended, Uncheckable int `json:"-"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Registrations[0].Status != statusUncheckable {
		t.Errorf("json status %q, want %q", doc.Registrations[0].Status, statusUncheckable)
	}
}

func TestJoinMCPStatusOnAMachineWithNoRegistrations(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	regs := scanPartyRegistrations(home, dir)
	if len(regs) != 0 {
		t.Fatalf("found registrations on an empty machine: %+v", regs)
	}
	var buf bytes.Buffer
	if !renderJoinMCPStatus(&buf, regs, true) {
		t.Error("an empty machine did not report clean")
	}
	var doc struct {
		Registrations []partyRegistration `json:"registrations"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("empty machine emitted invalid JSON: %v\n%s", err, buf.String())
	}
	if doc.Registrations == nil {
		t.Error("registrations is null rather than an empty list — a consumer's loop breaks on it")
	}
	if !strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Errorf("json output does not start with an object: %s", buf.String())
	}
}
