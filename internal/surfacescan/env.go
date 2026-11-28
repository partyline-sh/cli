package surfacescan

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Environment variables are the self-host configuration surface. They are also the one facet with
// no single declaration anywhere — they are read at ~40 call sites across Go and TypeScript, which
// is why the required set for `deploy/stack/env.example` originally had to be reconstructed by
// hand from source. Extracting them is what lets `.env.example` and `ptln server doctor` be
// generated instead of remembered (Epic H).

var (
	goEnvRe = regexp.MustCompile(`os\.Getenv\(\s*"([A-Z][A-Z0-9_]*)"\s*\)`)
	tsEnvRe = regexp.MustCompile(`process\.env(?:\.([A-Z][A-Z0-9_]*)|\[\s*"([A-Z][A-Z0-9_]*)"\s*\])`)
	// The one indirection in the TypeScript: `const env = (k: string) => (process.env[k] ?? "").trim()`
	// in slack-app.ts, then `env("SLACK_CLIENT_ID")`. Without this the three variables that gate
	// the entire Slack app are invisible to the extraction — which is not a cosmetic gap, because
	// features.json declaring a variable no extractor can see is a DRIFT ERROR. Deliberately
	// narrow: a bare `env("SHOUTY_NAME")` call and nothing else. A general "any function called
	// with a shouty string" rule would invent variables out of constants.
	tsEnvHelperRe = regexp.MustCompile(`(?:^|[^\w.])env\(\s*"([A-Z][A-Z0-9_]*)"\s*\)`)
)

func scanEnv(root string) []Item {
	readers := map[string][]string{} // VAR -> files that read it

	walk(root, func(name string) bool {
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, func(path string) {
		collect(root, path, goEnvRe, readers)
	})

	for _, sub := range []string{filepath.Join("web", "src"), filepath.Join("web", "scripts")} {
		walk(filepath.Join(root, sub), func(name string) bool {
			return (strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx") ||
				strings.HasSuffix(name, ".mjs") || strings.HasSuffix(name, ".js")) &&
				!strings.Contains(name, ".test.")
		}, func(path string) {
			collect(root, path, tsEnvRe, readers)
			collect(root, path, tsEnvHelperRe, readers)
		})
	}

	var out []Item
	for name, files := range readers {
		sort.Strings(files)
		// Cap the cited readers: a variable read in thirty places makes an unreadable entry, and
		// the useful signal is "who reads this at all", not an exhaustive index.
		if len(files) > 5 {
			files = append(files[:5:5], "…")
		}
		out = append(out, Item{
			Ref:    "env:" + name,
			Kind:   "env",
			Name:   name,
			Detail: files,
		})
	}
	return out
}

func collect(root, path string, re *regexp.Regexp, into map[string][]string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		name := m[1]
		if len(m) > 2 && name == "" {
			name = m[2] // the bracket form: process.env["X"]
		}
		if name == "" {
			continue
		}
		r := rel(root, path)
		if !contains(into[name], r) {
			into[name] = append(into[name], r)
		}
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
