// Trust · T2d — the daemon-hardcoded VISUAL render PRESET. When the web toggles visual verify on
// for a project but the repo has no `.partyline/visual` script, crank still needs a way to bring the
// UI up and screenshot it. This is that fallback: a render recipe OWNED BY THE DAEMON (this Go
// source), parameterized ONLY by the web's SAFE route DATA — never by any web-supplied code.
//
// LOAD-BEARING SECURITY LINE: the web supplies the TOGGLE + which routes to shoot; it must NEVER
// supply the render HOW. So the preset's shell/JS is entirely hardcoded here; the only variable
// input is the list of app paths, and those arrive through the environment (PARTYLINE_VISUAL_ROUTES,
// set by runRender) and are re-validated inside the JS — a route can never become a command.
//
// A preset resolves only when we can actually render: a recognized web framework in the committed
// package.json AND its tooling + Playwright present in the worktree's node_modules. If either is
// missing we return ok=false, and the gate WARNS ("no renderer") rather than failing or guessing.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// preset describes one hardcoded framework recipe: the human name, the dev-server command (fixed —
// run via `npx --no-install` so it can NEVER fetch/execute anything not already installed), and the
// port it listens on. `bin` is the node_modules binary that must exist for the recipe to be usable.
type preset struct {
	name string
	bin  string // node_modules/.bin entry that must exist (the framework CLI)
	// devCmd is the hardcoded server bring-up; ${PORT} is substituted by the shell, never by web data.
	devCmd string
	port   string
}

// presets are matched in order against the committed package.json's dependencies. Each command is a
// FIXED string — the framework name in package.json only SELECTS a recipe, it never becomes one.
var presets = []preset{
	// Next.js — reads PORT from the environment for `next dev`; we also pass -p explicitly.
	{name: "next.js", bin: "next", devCmd: `npx --no-install next dev -p "${PORT}"`, port: "3000"},
	// Vite — takes an explicit --port.
	{name: "vite", bin: "vite", devCmd: `npx --no-install vite --port "${PORT}" --strictPort`, port: "5173"},
	// Create React App — reads PORT + BROWSER from the environment.
	{name: "create-react-app", bin: "react-scripts", devCmd: `BROWSER=none npx --no-install react-scripts start`, port: "3000"},
}

// packageDeps returns the union of dependencies + devDependencies from a package.json (committed, in
// baseRepo — so which recipe we pick is decided by the TEAM's manifest, not a task's worktree edit).
func packageDeps(baseRepo string) map[string]bool {
	b, err := os.ReadFile(filepath.Join(baseRepo, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return nil
	}
	deps := map[string]bool{}
	for k := range pkg.Dependencies {
		deps[k] = true
	}
	for k := range pkg.DevDependencies {
		deps[k] = true
	}
	return deps
}

// hasNodeBin reports whether a node_modules/.bin entry exists in the WORKTREE (where the render runs)
// — i.e. dependencies are actually installed. A fresh git worktree has no node_modules unless the
// team provides it, so this is the honest "can we even render?" check.
func hasNodeBin(wtPath, bin string) bool {
	_, err := os.Stat(filepath.Join(wtPath, "node_modules", ".bin", bin))
	return err == nil
}

// hasPlaywright reports whether Playwright is installed in the worktree's node_modules (either the
// core `playwright` package or `@playwright/test`). The preset drives it to take the screenshots.
func hasPlaywright(wtPath string) bool {
	for _, p := range []string{"playwright", filepath.Join("@playwright", "test")} {
		if _, err := os.Stat(filepath.Join(wtPath, "node_modules", p)); err == nil {
			return true
		}
	}
	return false
}

// visualPreset resolves the daemon-hardcoded render recipe for a web-toggled project with no repo
// `.partyline/visual` script. It returns (script, true) only when a recognized framework AND its
// tooling + Playwright are present in the worktree; otherwise ("", false) so the gate WARNS instead
// of failing or executing a guess. `routes` is NOT interpolated into the script — it reaches the
// hardcoded JS purely via PARTYLINE_VISUAL_ROUTES (set by runRender) and is re-validated there.
func visualPreset(baseRepo, wtPath string, routes []string) (script string, ok bool) {
	deps := packageDeps(baseRepo)
	if deps == nil {
		return "", false
	}
	if !hasPlaywright(wtPath) {
		return "", false // no browser tooling → can't render (WARN, don't fail)
	}
	for _, p := range presets {
		if !deps[p.bin] || !hasNodeBin(wtPath, p.bin) {
			continue
		}
		return presetScript(p), true
	}
	return "", false
}

// presetScript assembles the FIXED render script for a recipe: start the dev server, wait for the
// port, then run a hardcoded Playwright screenshotter (a quoted heredoc — no shell expansion inside,
// so the JS is inert text). The JS reads the shots dir + routes from the environment and re-validates
// each route, so nothing web-supplied is ever executed. `p.devCmd`/`p.port` are compile-time
// constants from this file; only ${PORT} is shell-substituted.
func presetScript(p preset) string {
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString(`PORT="${PORT:-` + p.port + `}"` + "\n")
	b.WriteString("export PORT\n")
	// Start the server in the background; always tear it down on exit.
	b.WriteString(p.devCmd + " >/tmp/ptln-visual-server.log 2>&1 &\n")
	b.WriteString("__PTLN_SRV=$!\n")
	b.WriteString("trap 'kill \"$__PTLN_SRV\" 2>/dev/null || true' EXIT INT TERM\n")
	// Wait (bounded) for the server to answer on the port before we drive the browser.
	b.WriteString("for _i in $(seq 1 90); do\n")
	b.WriteString("  if curl -sf \"http://127.0.0.1:${PORT}/\" >/dev/null 2>&1; then break; fi\n")
	b.WriteString("  sleep 1\n")
	b.WriteString("done\n")
	// Hardcoded Playwright screenshotter. Quoted heredoc → the shell expands NOTHING inside; the JS
	// pulls the shots dir + routes from process.env and re-validates the routes itself.
	b.WriteString("node <<'PTLN_VISUAL_EOF'\n")
	b.WriteString(presetShotJS)
	b.WriteString("\nPTLN_VISUAL_EOF\n")
	return b.String()
}

// presetShotJS is the hardcoded Playwright screenshotter. It is INERT TEXT here (never assembled from
// web input): it reads PORT / PARTYLINE_SHOTS_DIR / PARTYLINE_VISUAL_ROUTES from the environment,
// re-validates each route to a strict app-path shape (defense-in-depth over the daemon's own gate),
// and screenshots each into the shots dir. No route is ever passed to a shell or eval.
const presetShotJS = `
const { chromium } = require('playwright');
(async () => {
  const port = process.env.PORT || '3000';
  const base = 'http://127.0.0.1:' + port;
  const dir = process.env.PARTYLINE_SHOTS_DIR;
  if (!dir) { console.error('no PARTYLINE_SHOTS_DIR'); process.exit(1); }
  // Routes arrive as DATA via the environment (never argv). Re-validate: must be an app path
  // starting with "/", safe chars only, no ".." segment. Anything else is dropped.
  const safe = (r) =>
    /^\/[A-Za-z0-9._~/-]*(\?[A-Za-z0-9._~=&%/-]*)?$/.test(r) &&
    !r.split('/').includes('..') && !r.split('/').includes('.');
  const raw = (process.env.PARTYLINE_VISUAL_ROUTES || '')
    .split('\n').map((s) => s.trim()).filter(Boolean);
  const routes = raw.filter(safe);
  const use = routes.length ? routes : ['/']; // default to the app root when no valid route
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    let n = 0;
    for (const r of use) {
      try {
        await page.goto(base + r, { waitUntil: 'networkidle', timeout: 30000 });
      } catch (e) {
        await page.goto(base + r, { waitUntil: 'domcontentloaded', timeout: 30000 }).catch(() => {});
      }
      const slug = (r.replace(/[^A-Za-z0-9]+/g, '_').replace(/^_+|_+$/g, '')) || 'root';
      const name = String(n).padStart(2, '0') + '_' + slug + '.png';
      await page.screenshot({ path: dir + '/' + name, fullPage: true });
      n++;
    }
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
`
