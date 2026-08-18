package surfacescan

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Next.js App Router puts the route in the FILESYSTEM, not in a table, so the directory tree is
// the authoritative list of what the product exposes. That is a gift for extraction: there is no
// router config to drift from reality.

var methodRe = regexp.MustCompile(`(?m)^export\s+(?:async\s+)?(?:function|const)\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b`)

// scanAPIRoutes lists every HTTP endpoint and the methods it actually exports. A route.ts that
// exports no handler is still reported (with no methods) — an endpoint that answers 405 to
// everything is a fact worth seeing, not something to hide.
func scanAPIRoutes(root string) []Item {
	base := filepath.Join(root, "web", "src", "app")
	var out []Item
	walk(base, func(name string) bool { return name == "route.ts" || name == "route.tsx" }, func(path string) {
		dir := filepath.Dir(path)
		route := routePath(base, dir)
		if !strings.HasPrefix(route, "/api") {
			return // a non-API route handler (rare); web routes are covered by page.tsx
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var methods []string
		for _, m := range methodRe.FindAllStringSubmatch(string(b), -1) {
			methods = append(methods, m[1])
		}
		out = append(out, Item{
			Ref:    "api:" + route,
			Kind:   "api",
			Name:   route,
			Detail: sortedUnique(methods),
			Source: rel(root, path),
		})
	})
	return out
}

// scanWebRoutes lists every rendered page.
func scanWebRoutes(root string) []Item {
	base := filepath.Join(root, "web", "src", "app")
	var out []Item
	walk(base, func(name string) bool { return name == "page.tsx" || name == "page.mdx" }, func(path string) {
		route := routePath(base, filepath.Dir(path))
		out = append(out, Item{
			Ref:    "web:" + route,
			Kind:   "web",
			Name:   route,
			Source: rel(root, path),
		})
	})
	return out
}

// routePath turns an App Router directory into the URL it serves.
//
// Two Next conventions have to be honoured or the extracted list would not match reality:
// parenthesised segments are ROUTE GROUPS that organise files without appearing in the URL
// (so `(marketing)/docs` serves `/docs`), and bracketed segments are dynamic and are kept
// verbatim so `[id]` reads the same in the extraction as it does in a doc's covers: claim.
func routePath(base, dir string) string {
	r, err := filepath.Rel(base, dir)
	if err != nil {
		return "/"
	}
	var segs []string
	for _, seg := range strings.Split(filepath.ToSlash(r), "/") {
		switch {
		case seg == "" || seg == ".":
		case strings.HasPrefix(seg, "(") && strings.HasSuffix(seg, ")"):
			// route group — organisational only, contributes nothing to the URL
		case strings.HasPrefix(seg, "@"):
			// parallel route slot — likewise invisible in the URL
		default:
			segs = append(segs, seg)
		}
	}
	if len(segs) == 0 {
		return "/"
	}
	return "/" + strings.Join(segs, "/")
}
