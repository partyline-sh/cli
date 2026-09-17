package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The review viewer is a browser surface built by an agent that cannot open a browser. These are the
// checks that catch what a headless author actually gets wrong: a script reaching for an element that
// the markup does not have, or a mark kind that renders with no colour because one CSS rule was
// missed when the palette changed. Neither shows up in `go build`, and both are invisible until a
// human opens the page and finds a dead button.

var idLookup = regexp.MustCompile(`getElementById\("([^"]+)"\)`)

func TestViewerScriptOnlyLooksUpElementsThatExist(t *testing.T) {
	html := string(reviewViewerHTML)
	for _, m := range idLookup.FindAllStringSubmatch(string(reviewViewerJS), -1) {
		id := m[1]
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("viewer.js looks up #%s, which viewer.html does not define — that control is dead", id)
		}
	}
}

// Every kind must be reachable from the toolbar, carry chrome styling in both themes, and have an
// ink for the overlay. A kind missing any one of those is a silently unusable button.
func TestEveryMarkKindIsFullyWired(t *testing.T) {
	html := string(reviewViewerHTML)
	js := string(reviewViewerJS)

	for _, kind := range []string{"scope", "behaviour", "constraint", "question"} {
		if !strings.Contains(html, `data-kind="`+kind+`"`) {
			t.Errorf("kind %q has no toolbar button", kind)
		}
		for _, tok := range []string{"--" + kind + "-fill", "--" + kind + "-ink"} {
			// Twice: once for light on :root, once inside the prefers-color-scheme block.
			if strings.Count(html, tok+":") < 2 {
				t.Errorf("token %s is not defined for both themes (found %d)", tok, strings.Count(html, tok+":"))
			}
		}
		if !strings.Contains(html, fmt.Sprintf("li[data-kind=%s] .num", kind)) {
			t.Errorf("kind %q has no sidebar chip colour", kind)
		}
		if !regexp.MustCompile(kind + `: "#[0-9a-fA-F]{6}"`).MatchString(js) {
			t.Errorf("kind %q has no overlay ink in KIND_INK", kind)
		}
	}
}

// The overlay ink is drawn ON the artifact, which has its own background and knows nothing about the
// viewer's theme. If it ever starts following prefers-color-scheme, marks become invisible on half
// the mockups people review — so the inks stay literals in the script, not CSS variables.
func TestOverlayInkIsThemeIndependent(t *testing.T) {
	js := string(reviewViewerJS)
	inkLine := ""
	for _, line := range strings.Split(js, "\n") {
		if strings.Contains(line, "var KIND_INK") {
			inkLine = line
			break
		}
	}
	if inkLine == "" {
		t.Fatal("KIND_INK is gone — the overlay has no ink source")
	}
	if strings.Contains(inkLine, "var(--") || strings.Contains(inkLine, "getComputedStyle") {
		t.Error("overlay ink must not come from a themed CSS variable; it is drawn on the artifact, not on our chrome")
	}
}

// The sandbox attribute is the entire isolation boundary for untrusted, agent-generated HTML.
// allow-same-origin alongside allow-scripts would let the artifact remove its own sandbox and read
// the page that embeds it.
func TestArtifactFrameStaysSandboxed(t *testing.T) {
	// Read the ATTRIBUTE, not the file: the prose above the iframe says "WITHOUT allow-same-origin",
	// and a substring search over the whole document matches that comment and fails on correct code.
	m := regexp.MustCompile(`<iframe[^>]*\bsandbox="([^"]*)"`).FindStringSubmatch(string(reviewViewerHTML))
	if m == nil {
		t.Fatal("the artifact iframe must carry a sandbox attribute")
	}
	tokens := strings.Fields(m[1])
	if len(tokens) != 1 || tokens[0] != "allow-scripts" {
		t.Errorf(`sandbox must be exactly "allow-scripts", got %q`, m[1])
	}
	for _, tok := range tokens {
		if tok == "allow-same-origin" {
			t.Error("allow-same-origin defeats the sandbox — the artifact could remove it and read the viewer")
		}
	}
	if !strings.Contains(artifactCSP, "connect-src 'none'") {
		t.Error("the artifact CSP must forbid connect-src so a mockup cannot phone home")
	}
}

// sdk.js is injected into the artifact document and must stay a courier: it answers questions and
// volunteers nothing. A wildcard-origin listener that acted on instructions would turn the artifact
// into the driver of the page reviewing it.
func TestSDKOnlyAnswersKnownMessages(t *testing.T) {
	sdk := string(reviewSDKJS)
	for _, forbidden := range []string{"eval(", "innerHTML", "document.write", "fetch(", "XMLHttpRequest"} {
		if strings.Contains(sdk, forbidden) {
			t.Errorf("sdk.js must not use %s — it runs inside untrusted artifact HTML", forbidden)
		}
	}
	if !strings.Contains(string(reviewViewerJS), "e.source !== frame.contentWindow") {
		t.Error("the viewer must authenticate postMessage by source; an opaque origin makes e.origin useless")
	}
}

// The viewer's JavaScript is embedded as bytes, so a syntax error in it is completely invisible to
// `go build` and `go test` — the binary compiles, ships, serves a broken page, and the first thing
// anyone knows about it is a blank screen. If node is on PATH, parse every module for real.
func TestViewerJavaScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — cannot parse-check the viewer modules")
	}
	entries, err := reviewAssets.ReadDir("assets/review")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		dst := filepath.Join(t.TempDir(), e.Name())
		if err := os.WriteFile(dst, reviewAsset(e.Name()), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(node, "--check", dst).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse:\n%s", e.Name(), out)
		}
		checked++
	}
	if checked == 0 {
		t.Error("no javascript modules found to check — the embed is empty or the path moved")
	}
}

// Every module the markup loads must exist in the embed, or the page 404s a script and silently
// loses whatever that file did.
func TestViewerLoadsOnlyEmbeddedModules(t *testing.T) {
	for _, m := range regexp.MustCompile(`src="/js/([^"]+)"`).FindAllStringSubmatch(string(reviewViewerHTML), -1) {
		if _, err := reviewAssets.ReadFile("assets/review/" + m[1]); err != nil {
			t.Errorf("viewer.html loads /js/%s, which is not embedded", m[1])
		}
	}
}

// The review URL is composed in three places — the CLI prints it, the MCP tool hands it to the
// user, and the daemon announces it at startup. One function so they cannot drift into disagreeing
// about the shape, which would send someone to a port nothing is listening on.
func TestReviewURLIsComposedOnce(t *testing.T) {
	got := ReviewURL("abc123")
	want := "http://127.0.0.1:" + strconv.Itoa(reviewPort()) + "/w/abc123"
	if got != want {
		t.Errorf("ReviewURL = %q, want %q", got, want)
	}
	// Loopback is the security boundary: the host serves a user's own artifacts using their own
	// token, and it is safe precisely because only someone already on the machine can reach it.
	if !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("review URL must be loopback-only, got %q", got)
	}
}

// The id lands in a URL path and then in an API request. Anything that is not id-shaped is refused
// before either happens.
func TestWorkItemIDGate(t *testing.T) {
	for _, ok := range []string{"0e0535b6-b322-4f40-bc65-b924046468a9", "abc12345", "ABCDEF01-2345"} {
		if !looksLikeWorkItemID(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"", "short", "../etc/passwd", "abc/def12345", "abc 12345", "zzzzzzzz", strings.Repeat("a", 65)} {
		if looksLikeWorkItemID(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// The gate has to be exercised THROUGH THE ROUTE, not just called directly. A unit test of
// looksLikeWorkItemID passes happily while the handler forgets to call it — which is exactly what a
// mutation run showed: disabling the check in the route left the direct test green.
func TestReviewRouteRefusesABadID(t *testing.T) {
	h := newReviewHost(nil, "", "") // nil client: a refused id must never reach a fetch
	srv := httptest.NewServer(h.handler())
	defer srv.Close()

	for _, bad := range []string{"/w/..%2fetc", "/w/not-hex-!!", "/w/short", "/w/" + strings.Repeat("a", 65)} {
		res, err := http.Get(srv.URL + bad)
		if err != nil {
			t.Fatalf("%s: %v", bad, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400 (a nil client means anything past the gate would panic)", bad, res.StatusCode)
		}
	}
}
