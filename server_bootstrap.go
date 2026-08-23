package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"partyline.sh/partyline/internal/features"
)

// server_bootstrap.go — `ptln server bootstrap`: check what a self-hosted box needs, then PRINT the
// exact ordered commands that bring it up. It never runs them.
//
// THE DECISION, stated once so it is not re-litigated: bootstrap is a REFERENCE, not a command that
// acts. It does not write /opt/partyline/.env, does not generate a secret into place, does not run
// `docker compose`, does not touch the database. The reason is the same one that made every other
// operator surface read-only: a tool that half-installs a box leaves an operator debugging OUR
// partial state instead of their own machine, and a plan they paste themselves is a plan they can
// read, diff, and keep. docs/epics/self-host.md's H.7 acceptance line was written as "fresh VM →
// working instance in one command" and has been corrected to match this.
//
// NEVER PRINT A VALUE. Like `ptln server doctor`, this reports variable NAMES and set/unset only —
// the output is meant to be pasted into an issue. Every probe is injected through bootstrapProbe so
// a test can hand it sentinel values and assert none of them come out, and so the "this writes
// nothing" test does not depend on what docker happens to be installed on the machine running it.

// installDir is where the stack lives on a box. Every path in the printed plan is under it.
const installDir = "/opt/partyline"

// The stack's published ports, from deploy/stack/docker-compose.yml. RELAY_PORT defaults to 2222;
// prod overrides it to 22, which is the operator's choice and not something to check for here.
var stackPorts = []int{80, 443, 2222}

// Minimum supported versions. docker 20.10 is the first release where `docker compose` (the v2
// plugin, not docker-compose the python script) is the normal way to run a stack.
const (
	minDockerMajor  = 20
	minDockerMinor  = 10
	minComposeMajor = 2
	minDiskGB       = 10
)

// bootstrapProbe is every question bootstrap asks about the machine. Injected so the whole report
// is testable without docker, without a network, and without depending on the host at all.
type bootstrapProbe struct {
	// look reads an environment variable (os.Getenv in production).
	look func(string) string
	// lookPath resolves a binary on PATH.
	lookPath func(string) (string, error)
	// run executes a read-only probe and returns trimmed combined output + ok.
	run func(name string, args ...string) (string, bool)
	// portBusy reports whether something is already answering on a port.
	portBusy func(port int) bool
	// envFileNames returns the variable NAMES set to a non-empty value in the box's env file.
	// Values are never returned, so they can never be printed.
	envFileNames func(path string) []string
	// stackDir locates a directory holding the stack's files (docker-compose.yml, env.example,
	// apply-migrations.sh), or reports that this machine has none.
	stackDir func() (string, bool)
}

func liveProbe() bootstrapProbe {
	return bootstrapProbe{
		look:     os.Getenv,
		lookPath: exec.LookPath,
		run: func(name string, args ...string) (string, bool) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err == nil
		},
		portBusy:     portBusy,
		envFileNames: envFileNames,
		stackDir:     findStackDir,
	}
}

func serverBootstrapMain(args []string) {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--help", "-h", "help":
			serverBootstrapHelp(os.Stdout)
			return
		default:
			fmt.Fprintf(os.Stderr, "ptln server bootstrap: unknown flag %q (flags: --json)\n", a)
			os.Exit(2)
		}
	}
	if !renderServerBootstrap(os.Stdout, liveProbe(), asJSON) {
		// Non-zero when a prerequisite is missing, so a script or an agent can branch on it
		// without parsing text. The plan is printed either way — it is what the operator came for.
		os.Exit(1)
	}
}

func serverBootstrapHelp(w io.Writer) {
	fmt.Fprintln(w, "ptln server bootstrap — the verified install plan for a self-hosted partyline box")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Checks docker, docker compose, the stack's ports, free disk, and which required")
	fmt.Fprintln(w, "  environment variables this box is missing — then prints the exact ordered")
	fmt.Fprintln(w, "  commands that bring the stack up.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  IT PRINTS. It never writes .env, never generates a secret, never runs docker,")
	fmt.Fprintln(w, "  and never touches the database. Run the commands yourself.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Variable NAMES and set/unset only — a value is never printed, so the output is")
	fmt.Fprintln(w, "  safe to paste into an issue.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Exit code 1 if any prerequisite is missing.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Flags: --json    the same report, machine-readable")
	fmt.Fprintln(w, "  See also: ptln server doctor (which features this box's environment configures)")
}

// bootCheck is one prerequisite line: what was checked, what was found, and the fix.
type bootCheck struct {
	Status string `json:"status"` // pass | warn | fail
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// renderServerBootstrap writes the report and reports whether every prerequisite passed.
func renderServerBootstrap(w io.Writer, p bootstrapProbe, asJSON bool) bool {
	var checks []bootCheck
	ok := true
	report := func(s checkStatus, what, detail, fix string) {
		name := map[checkStatus]string{ckPass: "pass", ckWarn: "warn", ckFail: "fail"}[s]
		checks = append(checks, bootCheck{name, what, detail, fix})
		if s == ckFail {
			ok = false
		}
	}

	checkDocker(p, report)
	checkPorts(p, report)
	checkDisk(p, report)
	stack := checkStack(p, report)
	missing := checkEnv(p, report)

	plan := bootstrapPlan(stack, missing)

	if asJSON {
		b, err := json.MarshalIndent(struct {
			Ready  bool        `json:"ready"`
			Checks []bootCheck `json:"checks"`
			Plan   []string    `json:"plan"`
		}{ok, checks, plan}, "", "  ")
		if err != nil { // unreachable for these types, but never print a half-written report
			fmt.Fprintln(w, "{}")
			return ok
		}
		fmt.Fprintln(w, string(b))
		return ok
	}

	fmt.Fprintln(w, "ptln server bootstrap — what this box needs, and the plan that gets it there")
	fmt.Fprintln(w, "(a plan only: nothing here is executed, no file is written, no secret is generated)")
	fmt.Fprintln(w)
	for _, c := range checks {
		glyph := map[string]string{"pass": "✓", "warn": "⚠", "fail": "✗"}[c.Status]
		fmt.Fprintf(w, "  %s %s", glyph, c.What)
		if c.Detail != "" {
			fmt.Fprintf(w, " — %s", c.Detail)
		}
		fmt.Fprintln(w)
		if c.Status != "pass" && c.Fix != "" {
			fmt.Fprintf(w, "      → %s\n", c.Fix)
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "PLAN — run these in order, on the box, as a user who can write "+installDir+":")
	fmt.Fprintln(w)
	n := 0
	for _, step := range plan {
		// An indented entry is a continuation of the step above it (the variables to fill in), not
		// a command of its own — numbering those would read as "run this".
		if strings.HasPrefix(step, "    ") {
			fmt.Fprintf(w, "      %s\n", strings.TrimSpace(step))
			continue
		}
		n++
		fmt.Fprintf(w, "  %2d. %s\n", n, step)
	}

	fmt.Fprintln(w)
	if ok {
		fmt.Fprintln(w, "Every prerequisite is present. Run the plan, then `ptln server doctor`.")
	} else {
		fmt.Fprintln(w, "Fix every ✗ above first — the plan assumes them. Then run this again.")
	}
	return ok
}

// ── prerequisite checks ─────────────────────────────────────────────────────────────────────────

type reportFn func(checkStatus, string, string, string)

func checkDocker(p bootstrapProbe, report reportFn) {
	if _, err := p.lookPath("docker"); err != nil {
		report(ckFail, "docker on PATH", "not found",
			"install docker engine: curl -fsSL https://get.docker.com | sh")
		// Nothing below can be asked without the binary, and guessing would print two more
		// failures for one cause.
		return
	}
	out, ran := p.run("docker", "--version")
	major, minor, parsed := parseVersion(out)
	switch {
	case !ran || !parsed:
		report(ckWarn, "docker version", "could not read `docker --version`",
			"check the install: docker --version")
	case major < minDockerMajor || (major == minDockerMajor && minor < minDockerMinor):
		report(ckFail, "docker version", fmt.Sprintf("%d.%d — older than the supported %d.%d", major, minor, minDockerMajor, minDockerMinor),
			"upgrade docker engine: curl -fsSL https://get.docker.com | sh")
	default:
		report(ckPass, "docker version", fmt.Sprintf("%d.%d", major, minor), "")
	}

	if _, ran := p.run("docker", "info", "--format", "{{.ServerVersion}}"); ran {
		report(ckPass, "docker daemon reachable", "", "")
	} else {
		report(ckFail, "docker daemon reachable", "`docker info` failed — the daemon is down, or this user is not in the docker group",
			"sudo systemctl start docker && sudo usermod -aG docker $USER (then log out and back in)")
	}

	out, ran = p.run("docker", "compose", "version")
	major, _, parsed = parseVersion(out)
	switch {
	case !ran:
		report(ckFail, "docker compose (v2 plugin)", "`docker compose version` failed — the v1 `docker-compose` script is not enough",
			"install the compose plugin: sudo apt-get install docker-compose-plugin")
	case !parsed:
		report(ckWarn, "docker compose version", "could not read `docker compose version`",
			"check the install: docker compose version")
	case major < minComposeMajor:
		report(ckFail, "docker compose version", fmt.Sprintf("v%d — the stack needs v%d", major, minComposeMajor),
			"install the compose plugin: sudo apt-get install docker-compose-plugin")
	default:
		report(ckPass, "docker compose version", fmt.Sprintf("v%d", major), "")
	}
}

func checkPorts(p bootstrapProbe, report reportFn) {
	for _, port := range stackPorts {
		what := fmt.Sprintf("port %d free", port)
		if !p.portBusy(port) {
			report(ckPass, what, "", "")
			continue
		}
		// A warn, not a fail: on a box that is already running the stack this is the stack
		// itself, and calling a working install broken is worse than making the operator look.
		report(ckWarn, what, "something is already listening",
			fmt.Sprintf("find it with `sudo lsof -i :%d` and stop it, or change the port in %s/docker-compose.yml", port, installDir))
	}
}

func checkDisk(p bootstrapProbe, report reportFn) {
	// df, not syscall.Statfs: one read-only command that works the same on every box the stack
	// runs on, and no build tags.
	out, ran := p.run("df", "-Pk", diskProbeDir())
	if !ran {
		report(ckWarn, "free disk", "could not read `df`", "check free space by hand: df -h "+installDir)
		return
	}
	freeGB, parsed := parseDfFreeGB(out)
	if !parsed {
		report(ckWarn, "free disk", "could not parse `df` output", "check free space by hand: df -h "+installDir)
		return
	}
	if freeGB < minDiskGB {
		report(ckFail, "free disk", fmt.Sprintf("%d GB free on %s — the images and the database want at least %d GB", freeGB, diskProbeDir(), minDiskGB),
			"free space, or attach a larger volume, before pulling the images")
		return
	}
	report(ckPass, "free disk", fmt.Sprintf("%d GB on %s", freeGB, diskProbeDir()), "")
}

// stackFiles are the three files a box needs out of deploy/stack/. The plan copies these; it must
// never name a path it has not confirmed, which is what "verified plan" means here.
var stackFiles = []string{"docker-compose.yml", "env.example", "apply-migrations.sh"}

// checkStack finds the stack's files on this machine and returns the directory holding them.
//
// It does NOT print a download URL. The stack lives in deploy/stack/ of the partyline monorepo,
// which is private and is PRUNED from the public CLI mirror (scripts/mirror-cli.sh removes deploy/
// wholesale), so there is no public URL to fetch it from — publishing one is self-host slice H.8.
// An earlier draft printed `curl` against raw.githubusercontent.com; every one of those three URLs
// 404s. A plan whose first three steps fail is worse than no plan, because the operator spends the
// failure believing our instructions and doubting their machine.
func checkStack(p bootstrapProbe, report reportFn) string {
	dir, ok := p.stackDir()
	if !ok {
		report(ckFail, "stack files (deploy/stack)", "not on this machine — they are not published for download yet (self-host slice H.8)",
			"copy deploy/stack/ here from a checkout of the partyline repo, or from a box already running the stack: scp -r you@box:"+installDir+" .")
		return ""
	}
	report(ckPass, "stack files (deploy/stack)", "found at "+dir, "")
	return dir
}

// checkEnv reports which required variables this box has not set, and returns their names in the
// order the plan should list them. Names only — a value is never read into the report.
func checkEnv(p bootstrapProbe, report reportFn) []string {
	set := map[string]bool{}
	for _, n := range p.envFileNames(installDir + "/.env") {
		set[n] = true
	}

	var missing []string
	for _, v := range requiredEnv() {
		if set[v] || strings.TrimSpace(p.look(v)) != "" {
			continue
		}
		missing = append(missing, v)
	}
	if len(missing) == 0 {
		report(ckPass, "required environment", fmt.Sprintf("all %d variables set", len(requiredEnv())), "")
		return nil
	}
	report(ckFail, "required environment",
		fmt.Sprintf("%d not set: %s", len(missing), strings.Join(missing, ", ")),
		"add each of those to "+installDir+"/.env (the plan below names them again, in place) — generate secrets with `openssl rand -base64 32`")
	return missing
}

// requiredEnv is every variable a box must set, taken from the same classification table that
// generates deploy/stack/env.example — so a new core variable appears here without anyone
// remembering to add it. notOnTheBox names the exceptions, each with the reason it is one.
func requiredEnv() []string {
	var out []string
	for _, c := range []features.Class{features.Core, features.Compose} {
		for _, n := range features.OfClass(c) {
			if _, skip := notOnTheBox[n.Name]; skip {
				continue
			}
			out = append(out, n.Name)
		}
	}
	sort.Strings(out)
	return out
}

// notOnTheBox: classified core/compose variables an operator must NOT be told to set, with why.
// Demanding one of these would send them hunting for a value that does not exist.
var notOnTheBox = map[string]string{
	"NEXT_PUBLIC_SUPABASE_ANON_KEY": "baked into the web image at build time on the CI runner",
	"NEXT_PUBLIC_SUPABASE_URL":      "baked into the web image at build time on the CI runner",
	"NEXT_RUNTIME":                  "set by Next itself at runtime",
	"NODE_ENV":                      "set by Next itself at runtime",
	"WEB_TAG":                       "compose defaults it; set it only to pin or roll back an image",
}

// ── the plan ────────────────────────────────────────────────────────────────────────────────────

// bootstrapPlan is the ordered command list. It names each missing variable on its own line so an
// operator (or an agent reading this output) knows exactly what to fill in — the NAME only.
//
// stack is the directory checkStack CONFIRMED holds the files, or "" when this machine has none.
// Every path the plan names has been checked to exist; nothing is fetched from a URL.
func bootstrapPlan(stack string, missing []string) []string {
	plan := []string{"sudo mkdir -p " + installDir + " && sudo chown $USER " + installDir}
	if stack == "" {
		plan = append(plan,
			"get deploy/stack/ onto this box — it is NOT published for download yet (self-host slice H.8):",
			"    from a checkout of the partyline repo, or from a machine that has one —",
			"    scp -r you@that-machine:path/to/partyline/deploy/stack .",
			"    then run `ptln server bootstrap` again from beside it: the next run finds the files",
			"    and prints the exact cp commands for steps 2-4, which this run cannot name honestly.")
	} else {
		plan = append(plan,
			"cp "+stack+"/docker-compose.yml "+installDir+"/docker-compose.yml",
			"cp "+stack+"/apply-migrations.sh "+installDir+"/apply-migrations.sh",
			"cp "+stack+"/env.example "+installDir+"/.env && chmod 600 "+installDir+"/.env")
	}
	// The .env is a copy of env.example, so a box without the stack files has nothing to edit yet —
	// say "once you have it" rather than pointing at a file that does not exist.
	envFile := installDir + "/.env"
	if stack == "" {
		envFile = installDir + "/.env (once step 2 gives you env.example to copy)"
	}
	if len(missing) == 0 {
		plan = append(plan, "edit "+envFile+" — every required variable is already set on this box")
	} else {
		plan = append(plan, fmt.Sprintf("edit %s and set %s (generate each secret with `openssl rand -base64 32`):",
			envFile, plural(len(missing), "this variable", "these variables")))
		for _, v := range missing {
			plan = append(plan, "    "+v+"=…")
		}
	}
	return append(plan,
		"cd "+installDir+" && docker compose pull",
		"docker compose up -d postgres    # the database first, so migrations have something to run against",
		"sh apply-migrations.sh           # halts on the first failure; nothing else starts until it passes",
		"docker compose up -d             # the rest of the stack",
		"ptln server doctor               # which features this box's environment configures",
	)
}

// ── probes ──────────────────────────────────────────────────────────────────────────────────────

// portBusy reports whether something already answers on a port. It DIALS rather than binding: a
// bind would briefly occupy the port it is asking about, which is a change, and this command makes
// none.
func portBusy(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// envFileNames returns the names set to a non-empty value in an env file. It deliberately returns
// NAMES: the values are compared against "" and discarded inside this function, so no caller can
// print one even by mistake. A missing file is the normal fresh-box case, not an error.
func envFileNames(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		// Trimmed-empty counts as UNSET, exactly as features.Status treats it: a trailing
		// newline in a secret is this repo's most-repeated outage.
		if name == "" || strings.TrimSpace(strings.Trim(value, `"'`)) == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// findStackDir looks for deploy/stack in this directory and every parent, so running bootstrap from
// anywhere inside a checkout finds it. A directory only counts when it holds ALL of stackFiles: a
// partial copy would produce a plan whose middle step fails, which is the failure mode this whole
// check exists to prevent. Stat only — nothing is opened, read, or written.
func findStackDir() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if complete(filepath.Join(dir, "deploy", "stack")) {
			return filepath.Join(dir, "deploy", "stack"), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// complete reports whether a directory holds every file the stack needs.
func complete(dir string) bool {
	for _, f := range stackFiles {
		if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.IsDir() {
			return false
		}
	}
	return true
}

// diskProbeDir is the deepest existing ancestor of the install dir — df needs a path that exists,
// and on a fresh box /opt/partyline does not yet.
func diskProbeDir() string {
	dir := installDir
	for dir != "/" && dir != "." {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		i := strings.LastIndex(dir, "/")
		if i <= 0 {
			break
		}
		dir = dir[:i]
	}
	return "/"
}

// parseVersion pulls the first "N.M" out of a version banner — "Docker version 27.3.1, build abc"
// and "Docker Compose version v2.29.7" both parse.
func parseVersion(s string) (major, minor int, ok bool) {
	for _, field := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' }) {
		field = strings.TrimPrefix(field, "v")
		parts := strings.Split(field, ".")
		if len(parts) < 2 {
			continue
		}
		maj, err1 := strconv.Atoi(parts[0])
		min, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		return maj, min, true
	}
	return 0, 0, false
}

// parseDfFreeGB reads the available column out of `df -Pk` output (POSIX format: the 4th column of
// the last line, in 1K blocks).
func parseDfFreeGB(out string) (int, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, false
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, false
	}
	kb, err := strconv.Atoi(fields[3])
	if err != nil {
		return 0, false
	}
	return kb / (1024 * 1024), true
}
