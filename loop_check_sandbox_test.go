package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func requireCheckSandbox(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
	case "linux":
		if _, err := exec.LookPath("bwrap"); err != nil {
			t.Skip("bubblewrap not installed; separate fail-closed regression covers this")
		}
	default:
		t.Skip("no native check sandbox on this platform")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	argv, err := checkSandboxCommand(runtime.GOOS, t.TempDir(), t.TempDir(), home, "true")
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		if runtime.GOOS == "darwin" {
			t.Fatalf("macOS sandbox failed: %v %s", err, output)
		}
		t.Skipf("native sandbox unavailable on this host: %v %s; fail-closed test still runs", err, output)
	}
}
func nativeCheck(t *testing.T, root, home, script string) (string, error) {
	t.Helper()
	scratch := t.TempDir()
	argv, err := checkSandboxCommand(runtime.GOOS, root, scratch, home, script)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = checksEnv(true)
	groupSpawn(cmd)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("sandbox check timed out")
	}
	return string(out), err
}

func TestBuilderCheckStoredCredentialIsolation(t *testing.T) {
	requireCheckSandbox(t)
	// Reproduce the original defect with a dummy stored credential, never real auth.
	home, root := t.TempDir(), t.TempDir()
	secret := filepath.Join(home, ".git-credentials")
	if err := os.WriteFile(secret, []byte("dummy-private-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "cat " + shellQuote(secret)
	before := runLoopCheck(root, command, nil, checksEnv(true))
	if before.Exit != 0 || !strings.Contains(before.Tail, "dummy-private-token") {
		t.Fatalf("original credential probe did not reproduce: %+v", before)
	}
	output, err := nativeCheck(t, root, home, command)
	if err == nil || strings.Contains(output, "dummy-private-token") {
		t.Fatalf("sandbox exposed the stored dummy credential: %v %q", err, output)
	}
	// Symlinks inside the project must not grant access to the host secret.
	if err := os.Symlink(secret, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	output, err = nativeCheck(t, root, home, "cat linked")
	if err == nil || strings.Contains(output, "dummy-private-token") {
		t.Fatalf("project symlink bypassed isolation: %v %q", err, output)
	}
}

func TestBuilderCheckWritesConfinedToProject(t *testing.T) {
	requireCheckSandbox(t)
	home, root := t.TempDir(), t.TempDir()
	marker := filepath.Join(home, "startup")
	output, err := nativeCheck(t, root, home, "printf project > result && printf changed > "+shellQuote(marker))
	if err == nil {
		t.Fatalf("host write allowed: %q", output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("host marker created: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "result")); err != nil || string(b) != "project" {
		t.Fatalf("project output unavailable: %q %v", b, err)
	}
}

func TestBuilderCheckCannotReachHostNetwork(t *testing.T) {
	requireCheckSandbox(t)
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl unavailable")
	}
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called.Store(true); fmt.Fprint(w, "private") }))
	defer server.Close()
	output, err := nativeCheck(t, t.TempDir(), t.TempDir(), shellQuote(curl)+" --noproxy '*' --connect-timeout 1 --max-time 2 -fsS "+shellQuote(server.URL))
	if err == nil || called.Load() {
		t.Fatalf("sandbox reached a host service: %v %q", err, output)
	}
}

func TestBuilderCheckSkipsStartupAndDirenv(t *testing.T) {
	requireCheckSandbox(t)
	root := t.TempDir()
	trap := filepath.Join(root, "login-shell")
	if err := os.WriteFile(trap, []byte("#!/bin/sh\nprintf unsafe-startup\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", trap)
	t.Setenv("BASH_ENV", trap)
	t.Setenv("ENV", trap)
	if err := os.WriteFile(filepath.Join(root, ".envrc"), []byte("echo unsafe-direnv"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runLoopCheckMode(root, "printf isolated", nil, checksEnv(true), true)
	if got.Exit != 0 || got.Tail != "isolated" {
		t.Fatalf("isolated startup: %+v", got)
	}
}

func TestBuilderCheckFailsClosedWithoutSandbox(t *testing.T) {
	// No executable is ever run on an unsupported OS, even with an existing project.
	if _, err := checkSandboxCommand("unsupported", t.TempDir(), t.TempDir(), t.TempDir(), "touch marker"); err == nil {
		t.Fatal("unsupported sandbox accepted")
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/usr/bin/bwrap"); err == nil {
			return
		}
		root := t.TempDir()
		got := runLoopCheckMode(root, "touch marker", nil, checksEnv(true), true)
		if got.Exit != -1 || !strings.Contains(got.Tail, "require bubblewrap") {
			t.Fatalf("missing sandbox: %+v", got)
		}
		if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
			t.Fatal("command ran without a sandbox")
		}
	}
}

func TestBuilderCheckUnavailableSandboxDoesNotRun(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	argv, err := checkSandboxCommand(runtime.GOOS, root, t.TempDir(), home, "true")
	if err == nil {
		if err := exec.Command(argv[0], argv[1:]...).Run(); err == nil {
			t.Skip("native sandbox works; other regressions execute inside it")
		}
	}
	got := runLoopCheckMode(root, "touch marker", nil, checksEnv(true), true)
	if got.Exit != -1 {
		t.Fatalf("sandbox failure was not an infrastructure error: %+v", got)
	}
	if problem := checksEnvProblem(root, loopCheckRound{Checks: []loopCheck{got}}); problem == "" {
		t.Fatal("sandbox failure could be ignored as an existing baseline failure")
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
		t.Fatal("command ran without a sandbox")
	}
}

func TestBuilderCheckRunsInstalledGoToolchain(t *testing.T) {
	requireCheckSandbox(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Go toolchain not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "check_test.go"), []byte("package check\nimport \"testing\"\nfunc TestValue(t *testing.T) { if 2+2 != 4 { t.Fatal(\"wrong\") } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Existing test initialization uses a scratch home. Resolve installed tool paths
	// explicitly without changing HOME or granting access to the rest of the real home.
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	argv, env, err := isolatedCheckCommandAt(root, "CGO_ENABLED=0 go test check_test.go", t.TempDir(), account.HomeDir, checksEnv(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir, command.Env = root, env
	groupSpawn(command)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "ok") {
		t.Fatalf("offline Go test failed inside sandbox: %v %s", err, output)
	}
}

func TestBuilderCheckRejectsBroadRootsAndToolSymlinks(t *testing.T) {
	home := t.TempDir()
	if _, err := checkSandboxCommand(runtime.GOOS, home, t.TempDir(), home, "true"); err == nil {
		t.Fatal("home directory accepted as writable project")
	}
	state := filepath.Join(home, ".partyline", "loops")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := checkSandboxCommand(runtime.GOOS, state, t.TempDir(), home, "true"); err == nil {
		t.Fatal("Partyline state accepted as project")
	}
	if err := os.MkdirAll(filepath.Join(home, "go", "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(home, "go", "pkg", "mod")); err != nil {
		t.Fatal(err)
	}
	for _, tool := range checkToolPaths(home) {
		if insideCheckPath(home, tool) {
			t.Fatalf("a tool directory symlink exposed the home: %v", tool)
		}
	}
}
func TestBuilderCheckCannotReadOtherTemporaryCredentialsOrWriteGitMetadata(t *testing.T) {
	requireCheckSandbox(t)
	home, root, other := t.TempDir(), t.TempDir(), t.TempDir()
	secret := filepath.Join(other, "dummy-key")
	if err := os.WriteFile(secret, []byte("dummy-other-private-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := nativeCheck(t, root, home, "cat "+shellQuote(secret))
	if err == nil || strings.Contains(output, "dummy-other-private-token") {
		t.Fatalf("other temporary credentials exposed: %v %q", err, output)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".git", "config")
	if err := os.WriteFile(config, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeCheck(t, root, home, "printf replaced > .git/config"); err == nil {
		t.Fatal("Git metadata was writable")
	}
	if b, err := os.ReadFile(config); err != nil || string(b) != "original" {
		t.Fatalf("Git config changed: %q %v", b, err)
	}
}

func TestBuilderCheckHTTPTestCannotBecomeIgnoredBaseline(t *testing.T) {
	requireCheckSandbox(t)
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	binary := filepath.Join(t.TempDir(), "http-check")
	source := filepath.Join(t.TempDir(), "http-check.go")
	program := `package main
import("fmt";"net/http";"net/http/httptest";"os")
func main(){
 defer func(){if e:=recover();e!=nil {fmt.Println(e);os.Exit(1)}}()
 s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){fmt.Fprint(w,"ok")}));defer s.Close()
 resp,err:=http.Get(s.URL);if err!=nil{fmt.Println(err);os.Exit(1)};resp.Body.Close()
 fmt.Println("httptest passed")
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(goTool, "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build httptest probe: %v %s", err, output)
	}
	root := t.TempDir()
	installed := filepath.Join(root, "http-check")
	bytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	got := runLoopCheckMode(root, "./http-check", nil, checksEnv(true), true)
	if runtime.GOOS == "darwin" {
		if got.Exit != -1 || !strings.Contains(got.Tail, "denied host access") {
			t.Fatalf("sandbox denial looked like an ordinary failed test: %+v", got)
		}
		// Seconds must not affect classification: a full Go check can run for minutes
		// before reporting a blocked httptest listener.
		got.Seconds = 12
		if problem := checksEnvProblem(root, loopCheckRound{Checks: []loopCheck{got}}); problem == "" {
			t.Fatal("sandbox-denied httptest could be ignored as a failing baseline")
		}
	} else if got.Exit != 0 || !strings.Contains(got.Tail, "httptest passed") {
		t.Fatalf("private Linux loopback did not work: %+v", got)
	}
}
func TestBuilderCheckRoundReusesPrivateCache(t *testing.T) {
	requireCheckSandbox(t)
	root, _ := loopRepo(t)
	got := runLoopChecksMode(root, []string{`printf cached > "$GOCACHE/probe"`, `test "$(cat "$GOCACHE/probe")" = cached`}, filepath.Join(t.TempDir(), "log"), checksEnv(true), true)
	if len(got.Checks) != 2 || got.Checks[0].Exit != 0 || got.Checks[1].Exit != 0 {
		t.Fatalf("checks did not share a private round cache: %+v", got)
	}
}

func TestBuilderCheckProductionBoundaryProtectsStoredCredentials(t *testing.T) {
	root, _ := loopRepo(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// The existing test initialization supplies a scratch home. Refuse to write
	// anywhere else, even if the test hook changes in future.
	if !strings.Contains(home, "ptln-test-home-") {
		t.Fatal("test requires the package scratch home")
	}
	credential := filepath.Join(home, "dummy-check-credential")
	if err := os.WriteFile(credential, []byte("dummy-boundary-private-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(credential) })
	round := realBuilderCheckRunner(root, []string{"cat " + shellQuote(credential)}, filepath.Join(t.TempDir(), "log"), checksEnv(true))
	if len(round.Checks) != 1 || round.Checks[0].Exit == 0 || strings.Contains(round.Checks[0].Tail, "dummy-boundary-private-token") {
		t.Fatalf("production check boundary exposed credentials: %+v", round)
	}
	if problem := checksEnvProblem(root, round); problem == "" {
		t.Fatal("native refusal did not pause the production check round")
	}
}

func TestBuilderCheckReadsGoInstalledOutsideDefaultToolTrees(t *testing.T) {
	requireCheckSandbox(t)
	home, root := t.TempDir(), t.TempDir()
	tool := filepath.Join(home, "actions-runner", "_work", "_tool", "go", "1.26.10", "arm64")
	bin := filepath.Join(tool, "bin")
	src := filepath.Join(tool, "src", "runtime")
	for _, dir := range []string{bin, src} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runtimeFile := filepath.Join(src, "runtime.go")
	if err := os.WriteFile(runtimeFile, []byte("installed-go-runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\ncat "+shellQuote(runtimeFile)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := nativeCheck(t, root, home, "go")
	if err != nil || output != "installed-go-runtime" {
		t.Fatalf("custom installed Go tree was not readable: %v %q", err, output)
	}
	secret := filepath.Join(home, "dummy-credential")
	if err := os.WriteFile(secret, []byte("unrelated-private-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = nativeCheck(t, root, home, "cat "+shellQuote(secret))
	if err == nil || strings.Contains(output, "unrelated-private-token") {
		t.Fatalf("allowing a toolchain exposed the rest of its home: %v %q", err, output)
	}
}

func TestBuilderCheckToolRootCannotExposeCredentials(t *testing.T) {
	home, err := resolvedCheckPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"/", filepath.Dir(home), home, filepath.Join(home, ".ssh"), filepath.Join(home, ".config"), filepath.Join(home, ".partyline", "loops"), filepath.Join(home, "Library", "Keychains")} {
		if safeCheckToolRoot(root, home) {
			t.Errorf("unsafe tool root accepted: %s", root)
		}
	}
	if !safeCheckToolRoot(filepath.Join(home, "actions-runner", "_work", "_tool", "go", "1.26.10", "arm64"), home) {
		t.Fatal("dedicated Go installation rejected")
	}
}

func TestBuilderCheckGoDiscoveryNeverExecutesPATHPrograms(t *testing.T) {
	home, bin, outside := t.TempDir(), t.TempDir(), t.TempDir()
	credentialDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(credentialDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "executed-before-sandbox")
	script := "#!/bin/sh\nprintf ran > " + shellQuote(marker) + "\nprintf '%s\\n' " + shellQuote(credentialDir) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, path := range checkToolPaths(home) {
		if strings.Contains(path, ".ssh") {
			t.Fatal("program output granted credential access")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("PATH program executed before entering the sandbox")
	}
}
