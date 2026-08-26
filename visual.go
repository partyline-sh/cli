// Trust · T2d — the VISUAL verify layer ("give crank eyes"). The headless worker cannot SEE what
// it renders, so plausible-but-wrong layout/CSS ships green (the board-column scroll took three
// tries). This gate gives the acceptance step actual sight: when a task's diff touches UI files, it
// brings the changed surface up in a headless browser, captures screenshot(s), and hands them to a
// VISION-capable adversarial reviewer that judges whether the rendered result really satisfies the
// task. A fail QUARANTINES like any other verify failure — no auto-merge of unverified visual work.
//
// Opt-in per repo (same reference-not-command shape as `.partyline/verify` / `.partyline/review`):
// a `.partyline/visual` file in the BASE repo holds a shell RENDER script. Its presence enables the
// gate; non-web projects simply don't have the file and skip it. Rendering (dev/preview server +
// Playwright + auth) is the TEAM'S data — it lives in their script, run in the worktree with
// $PARTYLINE_SHOTS_DIR pointing at the dir the script must drop PNG/JPG screenshots into. crank owns
// only detect → render → vision-review → quarantine; it never embeds a browser itself.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const visualFile = ".partyline/visual"

// visualCfg is the web's per-project T2d input, threaded from the run event through crank into the
// gate. It is the ONLY thing the control plane supplies: a boolean toggle and SAFE route DATA.
// It NEVER carries a render script — the render HOW stays repo-trusted (.partyline/visual) or a
// daemon-hardcoded preset (visual_preset.go). (Same RCE line as web-triggered updates.)
type visualCfg struct {
	on     bool     // web toggle: enable the gate even without a repo `.partyline/visual` file
	routes []string // safe app paths (already validated) the preset renderer screenshots
}

// uiFileExts is the heuristic for "this diff changed something that RENDERS" — the trigger for the
// visual gate. Deliberately focused on unambiguously-visual sources: over-triggering (e.g. every
// `.ts`) would render for pure-logic changes, under-triggering only degrades to the honest skip the
// worker already warns about. A team wanting stricter coverage pairs this with a `.partyline/verify`
// behavioral check.
var uiFileExts = map[string]bool{
	".tsx": true, ".jsx": true,
	".css": true, ".scss": true, ".sass": true, ".less": true,
	".html": true, ".htm": true,
	".vue": true, ".svelte": true, ".astro": true,
	".mdx": true,
}

// readVisual reports whether the visual gate is enabled for this repo (the base-repo file exists and
// has a non-empty render script) and returns that script. Full-line comments (`#…`) and blank lines
// are stripped; everything else is kept verbatim and run as one `sh -c` script, so a multi-line
// bring-up (start server, wait, drive Playwright) reads naturally. Read from the BASE repo — a task
// can't weaken the gate it's judged against by editing its own worktree copy.
func readVisual(baseRepo string) (enabled bool, script string) {
	b, err := os.ReadFile(filepath.Join(baseRepo, visualFile))
	if err != nil {
		return false, ""
	}
	var lines []string
	for _, ln := range strings.Split(string(b), "\n") {
		if t := strings.TrimSpace(ln); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		lines = append(lines, ln)
	}
	script = strings.TrimSpace(strings.Join(lines, "\n"))
	return script != "", script
}

// changedFiles lists the paths in the task's committed change (its single crank commit). Empty on
// any git error (no parent, not a repo) — the caller treats "can't tell what changed" as "no UI
// change detected" and skips, which is safe: the objective + textual gates still ran.
func changedFiles(wtPath string) []string {
	out, err := exec.Command("git", "-C", wtPath, "diff", "--name-only", "HEAD^", "HEAD").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, ln := range strings.Split(string(out), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			files = append(files, ln)
		}
	}
	return files
}

// touchesUI is true when any changed path is an unambiguously-visual source (see uiFileExts) — the
// signal that a render+look is worth the cost.
func touchesUI(files []string) bool {
	for _, f := range files {
		if uiFileExts[strings.ToLower(filepath.Ext(f))] {
			return true
		}
	}
	return false
}

// runRender executes the team's render script in the worktree with $PARTYLINE_SHOTS_DIR set to the
// dir it must write screenshots into. Bounded by timeout; its output tail is captured for the human
// on failure. The script owns EVERYTHING about bringing the app up and driving the browser.
func runRender(wtPath, script, shotsDir string, routes []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", script)
	c.Dir = wtPath
	// PARTYLINE_SHOTS_DIR = where to drop screenshots. PARTYLINE_VISUAL_ROUTES = the SAFE route DATA
	// (newline-joined), passed via ENV so a route is never interpolated into the shell/argv — the
	// hardcoded preset (and a repo script that wants it) reads it as data. Never an executable value.
	c.Env = append(os.Environ(),
		"PARTYLINE_SHOTS_DIR="+shotsDir,
		"PARTYLINE_VISUAL_ROUTES="+strings.Join(routes, "\n"),
	)
	out, err := c.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("render timed out (>%s)", timeout)
	}
	if err != nil {
		return fmt.Errorf("render script failed: %v\n%s", err, tailString(string(out), 1200))
	}
	return nil
}

// shotExts are the screenshot formats the reviewer can look at (matched case-insensitively).
var shotExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true}

// collectShots returns the screenshot files the render script dropped in dir (PNG/JPG), sorted for a
// stable review order. It enumerates the dir once and filters by lowercased extension — so a
// case-insensitive filesystem (macOS) can't double-count `x.PNG` under both `*.png` and `*.PNG`.
// Empty → the render "succeeded" but produced nothing to look at, which the caller treats as a
// fail-closed quarantine (visual work that can't be seen isn't verified).
func collectShots(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var shots []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if shotExts[strings.ToLower(filepath.Ext(e.Name()))] {
			shots = append(shots, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(shots)
	return shots
}

// visualReviewerPrompt frames the adversarial VISION review: the reviewer must READ (look at) each
// screenshot and judge whether the rendered result satisfies the task, ending with the same VERDICT
// line the textual reviewer uses (so parseReviewVerdict handles both).
func visualReviewerPrompt(task string, shots []string, rubric string) string {
	var b strings.Builder
	b.WriteString("You are an ADVERSARIAL VISUAL reviewer for a UI change. Look at SCREENSHOTS of the rendered result and find reasons the rendered UI does NOT correctly satisfy the task. Be skeptical; do not give the benefit of the doubt.\n\n")
	b.WriteString("The worker that made this change was HEADLESS and could NOT see what it rendered, so plausible-but-broken layout/CSS is exactly what you are here to catch: overlap, clipping, zero-height or collapsed regions, overflow, content that should scroll but doesn't, misalignment, unreadable contrast, missing elements.\n\n")
	b.WriteString("TASK (what the change was supposed to achieve):\n")
	b.WriteString(task)
	b.WriteString("\n\nSCREENSHOTS of the rendered result — use the Read tool to open and LOOK AT each of these image files before judging:\n")
	for _, s := range shots {
		b.WriteString("- ")
		b.WriteString(s)
		b.WriteString("\n")
	}
	if rubric != "" {
		b.WriteString("\nADDITIONAL REVIEW GUIDANCE (from the team):\n")
		b.WriteString(rubric)
		b.WriteString("\n")
	}
	b.WriteString("\nJudge ONLY whether the RENDERED result visibly satisfies the task. If a screenshot shows the layout is broken or the task's visual goal is not met, FAIL. If you cannot open a screenshot, FAIL (fail-closed).\n")
	// Reason-before-deciding and verification honesty apply here too: a visual verdict given before
	// actually looking at every screenshot is the failure this gate exists to prevent. The code
	// calibration examples do not fit a visual judgement, so only the two that do are included.
	b.WriteString("\n" + reviewThinkFirst + "\n\n" + reviewHonesty + "\n")
	b.WriteString("\nEnd your reply with EXACTLY one line:\nVERDICT: PASS\nor\nVERDICT: FAIL — <one-line reason>\n")
	return b.String()
}

// runVisualReview is the T2d gate. The gate is ON when the repo opted in (`.partyline/visual`) OR the
// web toggled it for this project (vc.on). ran=false when it's off, when the diff touched no UI files
// (honest skip), or when the web toggle is on but no renderer resolves (WARN, not a failure — see
// verifyResult.warn). Otherwise it renders (repo script or hardcoded preset), collects screenshots,
// and asks a vision-capable independent reviewer for a verdict — FAIL-CLOSED on a render failure, no
// screenshots, engine error, timeout, or an unparseable reply. The web NEVER supplies the render
// script: the HOW is always the repo-trusted script or the daemon-hardcoded preset (visual_preset.go).
func runVisualReview(baseRepo, wtPath, task string, timeout time.Duration, vc visualCfg) verifyResult {
	repoEnabled, script := readVisual(baseRepo)
	// The gate is ON when the repo opted in (a base `.partyline/visual` script) OR the web toggled it
	// for this project (vc.on). Neither → the gate is off entirely (honest skip, not a pass).
	if !repoEnabled && !vc.on {
		return verifyResult{ran: false}
	}
	if !touchesUI(changedFiles(wtPath)) {
		return verifyResult{ran: false} // no UI surface changed → nothing to render
	}
	// Resolve the render HOW — NEVER from the web. The repo-trusted script wins; else, when the web
	// toggle turned the gate on, fall back to a daemon-hardcoded framework preset parameterized ONLY
	// by the web's route DATA. A toggle with no repo script AND no resolvable preset must not fail the
	// run or execute anything web-supplied: it WARNS and skips (ran=false).
	if !repoEnabled {
		preset, ok := visualPreset(baseRepo, wtPath, vc.routes)
		if !ok {
			return verifyResult{ran: false, warn: "visual verify on, but no renderer resolved (no .partyline/visual script and no supported framework preset with deps) — skipped"}
		}
		script = preset
	}

	// Screenshots land INSIDE the worktree so the reviewer's Read tool (cwd = worktree) can open
	// them without extra --add-dir grants. It's a temp dir, never committed; removed after review.
	shotsDir, err := os.MkdirTemp(wtPath, ".ptln-shots-")
	if err != nil {
		return verifyResult{ran: true, ok: false, reasons: "visual: couldn't create a screenshot dir — quarantined (fail-closed)"}
	}
	defer os.RemoveAll(shotsDir)

	if err := runRender(wtPath, script, shotsDir, vc.routes, timeout); err != nil {
		return verifyResult{ran: true, ok: false, reasons: "visual: " + err.Error()}
	}
	shots := collectShots(shotsDir)
	if len(shots) == 0 {
		return verifyResult{ran: true, ok: false, reasons: "visual: the render script produced no screenshot to review — quarantined (fail-closed)"}
	}

	// A fresh claude with ONLY the Read tool (to open the images — Read renders them visually) and no
	// thread: it shares no context with the producer and can't edit the branch. Independence + sight.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", visualReviewerPrompt(task, shots, reviewRubric(baseRepo)),
		"--output-format", "json", "--allowedTools", "Read")
	cmd.Dir = wtPath
	cmd.Env = append(os.Environ(), "PARTYLINE=1") // no thread wiring — independent of the producer
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return verifyResult{ran: true, ok: false, reasons: fmt.Sprintf("visual: reviewer timed out (>%s) — quarantined", timeout)}
	}
	if err != nil {
		return verifyResult{ran: true, ok: false, reasons: "visual: couldn't run the vision reviewer — quarantined (fail-closed)"}
	}
	reply, _, _, ok := parseWorkerOutput(out)
	if !ok {
		reply = string(out)
	}
	pass, reasons := parseReviewVerdict(reply)
	if !pass && reasons != "" {
		reasons = strings.Replace(reasons, "reviewer:", "visual reviewer:", 1)
	}
	return verifyResult{ran: true, ok: pass, reasons: reasons}
}

// reviewRubric threads any `.partyline/review` rubric into the visual reviewer too, so a team's extra
// guidance applies to both the textual and the visual judgment. Absent → "".
func reviewRubric(baseRepo string) string {
	_, rubric := readReview(baseRepo)
	return rubric
}
