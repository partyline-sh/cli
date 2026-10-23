package surfacegen

import (
	"net/url"
	"path"
	"strings"
	"testing"

	"partyline.sh/partyline/internal/clispec"
)

// The URL is the whole deliverable for a reader who has installed nothing, so it is pinned from
// both ends: the constant the document prints and the path the file is generated to. Nothing else
// keeps them equal — `web/public/` is served verbatim, so renaming the artifact silently moves the
// address every prompt and bookmark points at.
func TestPublishedURLMatchesTheGeneratedPath(t *testing.T) {
	u, err := url.Parse(fullURL)
	if err != nil {
		t.Fatalf("fullURL is not a URL: %v", err)
	}
	if u.Scheme != "https" {
		t.Errorf("fullURL scheme = %q, want https", u.Scheme)
	}
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := "web/public" + u.Path
	var found bool
	for _, f := range files {
		if f.Path == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("nothing is generated to %q, so %s serves nothing (or a stale hand-written file)", want, fullURL)
	}
	if path.Ext(u.Path) != ".txt" {
		t.Errorf("%s does not end in .txt — the extension is what makes it plain text over HTTP", fullURL)
	}
}

// A document nobody can find is not published. llms.txt is the file a model reaches for first, so
// it is the one place the full document MUST be linked; and the full document names its own
// address, because it gets copied into prompts far from this repo.
func TestBothFilesPointAtTheStableURL(t *testing.T) {
	for name, body := range llmsFiles(t) {
		if !strings.Contains(body, fullURL) {
			t.Errorf("%s never mentions %s — the reader is left with no way to fetch the full document", name, fullURL)
		}
	}
}

// The three questions an agent is asked by someone evaluating partyline, and the three it currently
// gets wrong from a command list alone. Each assertion below is a real wrong answer we are buying
// insurance against, not a spelling check.
func TestFullDocumentAnswersWhatAnEvaluatingAgentIsAsked(t *testing.T) {
	body := llmsFiles(t)["web/public/llms-full.txt"]
	lower := strings.ToLower(body)

	// 1. "I filed the plan — is it building?" No.
	if !strings.Contains(lower, "filing is not starting") || !strings.Contains(lower, "promoted") {
		t.Error("llms-full.txt does not say that filing a plan starts nothing until it is promoted")
	}
	// 2. "What is a context thread for?" Durable decisions — not a transcript.
	if !strings.Contains(lower, "context thread") ||
		!strings.Contains(lower, "not a chat log") ||
		!strings.Contains(lower, "decision") {
		t.Error("llms-full.txt does not explain what a context thread holds, or that it is not a transcript")
	}
	// 3. "Which commands exist?" All of them, including subcommands.
	for _, c := range clispec.Commands {
		if c.Hidden {
			continue
		}
		if !strings.Contains(body, "### ptln "+c.Name) {
			t.Errorf("llms-full.txt omits `ptln %s`", c.Name)
		}
		for _, sub := range c.Subs {
			name, _, _ := strings.Cut(sub, ": ")
			if !strings.Contains(body, "`ptln "+c.Name+" "+name+"`") {
				t.Errorf("llms-full.txt omits `ptln %s %s`", c.Name, name)
			}
		}
	}
}

func llmsFiles(t *testing.T) map[string]string {
	t.Helper()
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range files {
		if strings.HasPrefix(f.Path, "web/public/llms") {
			out[f.Path] = string(f.Body)
		}
	}
	if len(out) != 2 {
		t.Fatalf("expected llms.txt and llms-full.txt, got %d", len(out))
	}
	return out
}
