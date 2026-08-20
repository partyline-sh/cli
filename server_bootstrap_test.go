package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readyProbe is a box where everything bootstrap asks about is present.
func readyProbe() bootstrapProbe {
	set := map[string]bool{}
	for _, v := range requiredEnv() {
		set[v] = true
	}
	return bootstrapProbe{
		look: func(name string) string {
			if set[name] {
				return "value-for-" + name
			}
			return ""
		},
		lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		run: func(name string, args ...string) (string, bool) {
			switch {
			case name == "docker" && args[0] == "--version":
				return "Docker version 27.3.1, build abc1234", true
			case name == "docker" && args[0] == "info":
				return "27.3.1", true
			case name == "docker" && args[0] == "compose":
				return "Docker Compose version v2.29.7", true
			case name == "df":
				return "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000000 20000000 180000000 11% /", true
			}
			return "", false
		},
		portBusy:     func(int) bool { return false },
		envFileNames: func(string) []string { return nil },
		stackDir:     func() (string, bool) { return "/src/deploy/stack", true },
	}
}

// bareProbe is a fresh VM with nothing on it: no docker, a busy port, a full disk, no configuration.
func bareProbe() bootstrapProbe {
	return bootstrapProbe{
		look:         func(string) string { return "" },
		lookPath:     func(string) (string, error) { return "", fmt.Errorf("not found") },
		run:          func(string, ...string) (string, bool) { return "", false },
		portBusy:     func(int) bool { return true },
		envFileNames: func(string) []string { return nil },
		stackDir:     func() (string, bool) { return "", false },
	}
}

func TestBootstrapOnABareBoxFailsAndNamesEveryFix(t *testing.T) {
	var b strings.Builder
	if renderServerBootstrap(&b, bareProbe(), false) {
		t.Fatal("a box with no docker and no configuration reported ready — bootstrap would send an operator into a broken install")
	}
	out := b.String()
	for _, want := range []string{"✗ docker on PATH", "get.docker.com", "✗ required environment",
		"✗ stack files (deploy/stack)", "not published for download yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Every required variable must be named — that is the whole point of the check.
	for _, v := range requiredEnv() {
		if !strings.Contains(out, v) {
			t.Errorf("required variable %s is unset on this box but never named", v)
		}
	}
	// And every failing/warning line must carry a fix arrow. TestEveryFailureNamesAFix guards the
	// source; this guards what actually reaches the terminal.
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "  ✗ ") && !strings.HasPrefix(l, "  ⚠ ") {
			continue
		}
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "      → ") {
			t.Errorf("no fix under %q", l)
		}
	}
}

func TestBootstrapOnAReadyBoxPrintsTheOrderedPlan(t *testing.T) {
	var b strings.Builder
	if !renderServerBootstrap(&b, readyProbe(), false) {
		t.Fatalf("a box with every prerequisite reported not ready:\n%s", b.String())
	}
	out := b.String()
	for _, want := range []string{
		"PLAN",
		"   1. sudo mkdir -p /opt/partyline",
		"cp /src/deploy/stack/docker-compose.yml /opt/partyline/docker-compose.yml",
		"cp /src/deploy/stack/env.example /opt/partyline/.env",
		"docker compose pull",
		"apply-migrations.sh",
		"ptln server doctor",
		"Every prerequisite is present",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The plan is ordered, and its order is the point: pull before up, database before migrations,
	// migrations before the rest of the stack.
	for _, pair := range [][2]string{
		{"docker compose pull", "docker compose up -d postgres"},
		{"docker compose up -d postgres", "sh apply-migrations.sh"},
		{"sh apply-migrations.sh", "docker compose up -d  "},
	} {
		if strings.Index(out, pair[0]) > strings.Index(out, pair[1]) {
			t.Errorf("plan is out of order: %q must come before %q", pair[0], pair[1])
		}
	}
}

// The guardrail, asserted rather than asserted-about: nothing bootstrap prints is a value.
func TestBootstrapNeverPrintsAValue(t *testing.T) {
	const sentinel = "SUPER-SECRET-SENTINEL"
	p := readyProbe()
	p.look = func(string) string { return sentinel }
	p.envFileNames = func(string) []string { return []string{"POSTGRES_PASSWORD"} }

	for _, asJSON := range []bool{false, true} {
		var b strings.Builder
		renderServerBootstrap(&b, p, asJSON)
		if strings.Contains(b.String(), sentinel) {
			t.Errorf("json=%v: a value reached the output — this report is meant to be pasted into an issue:\n%s", asJSON, b.String())
		}
	}
}

// envFileNames reads a box's .env. It must return NAMES and never a value, and must treat a
// whitespace-only value as unset (the trailing-newline outage this repo keeps having).
func TestEnvFileNamesReturnsNamesNotValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# a comment\n\nPOSTGRES_PASSWORD=hunter2\nexport SITE_URL=\"https://example.com\"\nBLANK=\nWHITESPACE=   \nnot-an-assignment\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(envFileNames(path), ",")
	if got != "POSTGRES_PASSWORD,SITE_URL" {
		t.Errorf("envFileNames = %q, want POSTGRES_PASSWORD,SITE_URL", got)
	}
	for _, value := range []string{"hunter2", "https://example.com"} {
		if strings.Contains(got, value) {
			t.Errorf("a value leaked out of envFileNames: %q", value)
		}
	}
	if names := envFileNames(filepath.Join(dir, "nope.env")); names != nil {
		t.Errorf("a missing env file is the normal fresh-box case, got %v", names)
	}
}

// THE GUARDRAIL THAT MATTERS: bootstrap writes nothing. Run it for real — the live probe, the
// machine's actual docker and df — against a temp HOME and a temp working directory, and assert the
// tree is byte-identical afterwards.
func TestBootstrapWritesNothing(t *testing.T) {
	home := t.TempDir()
	// Seed a plausible tree so "identical" is a claim about content, not about emptiness.
	if err := os.MkdirAll(filepath.Join(home, ".partyline"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".partyline/token":   "a-token",
		".partyline/env":     "SESSION_JWT_SECRET=x\n",
		"docker-compose.yml": "services: {}\n",
	} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Chdir(home)

	before := treeHash(t, home)
	for _, asJSON := range []bool{false, true} {
		renderServerBootstrap(io.Discard, liveProbe(), asJSON)
	}
	if after := treeHash(t, home); after != before {
		t.Errorf("bootstrap changed the filesystem — it must only print.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// treeHash renders every path under root with its mode and content hash, so any create, delete,
// truncate, chmod or edit shows up as a difference.
func treeHash(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s mode=%s dir=%v", rel, info.Mode(), d.IsDir())
		if !d.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			fmt.Fprintf(&b, " sha=%s", hex.EncodeToString(sum[:]))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestParseVersionAndDf(t *testing.T) {
	for _, tc := range []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"Docker version 27.3.1, build abc1234", 27, 3, true},
		{"Docker Compose version v2.29.7", 2, 29, true},
		{"Docker version 19.03.15, build 99e3ed8", 19, 3, true},
		{"", 0, 0, false},
		{"garbage with no version", 0, 0, false},
	} {
		major, minor, ok := parseVersion(tc.in)
		if major != tc.major || minor != tc.minor || ok != tc.ok {
			t.Errorf("parseVersion(%q) = %d,%d,%v want %d,%d,%v", tc.in, major, minor, ok, tc.major, tc.minor, tc.ok)
		}
	}
	gb, ok := parseDfFreeGB("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000000 20000000 20971520 11% /")
	if !ok || gb != 20 {
		t.Errorf("parseDfFreeGB = %d,%v want 20,true", gb, ok)
	}
	if _, ok := parseDfFreeGB("nonsense"); ok {
		t.Error("parseDfFreeGB accepted nonsense")
	}
}

// An old docker is a fail with an upgrade command, not a silent pass.
func TestBootstrapRejectsAnUnsupportedDocker(t *testing.T) {
	p := readyProbe()
	p.run = func(name string, args ...string) (string, bool) {
		if name == "docker" && args[0] == "--version" {
			return "Docker version 19.03.15, build 99e3ed8", true
		}
		return readyProbe().run(name, args...)
	}
	var b strings.Builder
	if renderServerBootstrap(&b, p, false) {
		t.Fatal("docker 19.03 reported ready")
	}
	if !strings.Contains(b.String(), "older than the supported 20.10") {
		t.Errorf("the version failure does not say what is wrong:\n%s", b.String())
	}
}

// THE REVIEWER'S FINDING, turned into a ratchet. The first draft's plan told the operator to curl
// three files from raw.githubusercontent.com/partyline-sh/partyline — a PRIVATE repo whose deploy/
// directory scripts/mirror-cli.sh prunes from the public mirror. All three 404, so the plan failed
// at step 2 and every operator would have spent that failure doubting their own machine.
//
// The rule now: the plan may only name a path bootstrap has CONFIRMED exists. No URL, from any
// state of the box.
func TestThePlanNeverFetchesFromAURL(t *testing.T) {
	for name, p := range map[string]bootstrapProbe{"ready": readyProbe(), "bare": bareProbe()} {
		var b strings.Builder
		renderServerBootstrap(&b, p, false)
		out := b.String()
		// Scoped to the PLAN. A fix line may legitimately point at a third party's own installer
		// (docker's `curl https://get.docker.com | sh`); what must never happen is the plan
		// fetching OUR artifacts from a URL, because we publish none.
		plan := out
		if i := strings.Index(out, "PLAN —"); i >= 0 {
			plan = out[i:]
		}
		for _, banned := range []string{"raw.githubusercontent.com", "curl ", "wget "} {
			if strings.Contains(plan, banned) {
				t.Errorf("%s box: the plan reaches for %q — every artifact it names must be one this "+
					"command checked exists on the machine:\n%s", name, banned, out)
			}
		}
		if strings.Contains(out, "raw.githubusercontent.com") {
			t.Errorf("%s box: raw.githubusercontent.com is a 404 for deploy/ — the repo is private "+
				"and scripts/mirror-cli.sh prunes deploy/ from the public mirror:\n%s", name, out)
		}
	}
}

// A partial deploy/stack is not a stack: naming it would produce a plan whose middle step fails.
func TestFindStackDirRequiresEveryFile(t *testing.T) {
	root := t.TempDir()
	stack := filepath.Join(root, "deploy", "stack")
	if err := os.MkdirAll(filepath.Join(root, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stack, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, "sub", "dir"))

	if _, ok := findStackDir(); ok {
		t.Error("an empty deploy/stack counted as the stack")
	}
	for i, f := range stackFiles {
		if err := os.WriteFile(filepath.Join(stack, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		dir, ok := findStackDir()
		wantOK := i == len(stackFiles)-1
		if ok != wantOK {
			t.Fatalf("after writing %d of %d files, findStackDir ok = %v, want %v", i+1, len(stackFiles), ok, wantOK)
		}
		// Walking up from sub/dir must find the root's deploy/stack, not invent a path.
		if ok {
			want, _ := filepath.EvalSymlinks(stack)
			got, _ := filepath.EvalSymlinks(dir)
			if got != want {
				t.Errorf("findStackDir = %q, want %q", got, want)
			}
		}
	}
}
