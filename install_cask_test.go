package main

// Tests for the Homebrew cask's `preflight` verification block, which lives in
// .goreleaser.yaml's homebrew_casks[].custom_block and is what brew users get instead of
// install.sh. The block is READ OUT OF THE CONFIG at test time and executed by
// cask_preflight_harness_test.rb, so this cannot drift from what ships.
//
// Same property as the install.sh tests: fail closed. A bad signature, a signature that doesn't
// cover the sha256 this cask would install, a missing signature asset, an unusable cosign or an
// unpinned cask must all raise out of preflight — which runs before Homebrew moves any binary
// into place, so a raise means nothing is installed.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const caskAsset = "partyline_1.2.3_darwin_arm64.tar.gz"

// caskCustomBlock pulls the Ruby out of .goreleaser.yaml.
func caskCustomBlock(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootForTest(t), ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		HomebrewCasks []struct {
			CustomBlock string `yaml:"custom_block"`
		} `yaml:"homebrew_casks"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.HomebrewCasks) == 0 || strings.TrimSpace(cfg.HomebrewCasks[0].CustomBlock) == "" {
		t.Fatal("no homebrew_casks[0].custom_block in .goreleaser.yaml")
	}
	return cfg.HomebrewCasks[0].CustomBlock
}

// caskFixture is a release as the cask sees it: assets served under /<tag>/<name>.
type caskFixture struct {
	assets map[string][]byte
}

// newCaskFixture builds a coherent release whose signed checksums.txt covers assetSha for
// caskAsset, signed (with the toy scheme from install_script_test.go) by the expected identity.
func newCaskFixture(assetSha string) *caskFixture {
	checksums := []byte(fmt.Sprintf("%s  %s\n", assetSha, caskAsset))
	f := &caskFixture{assets: map[string][]byte{
		"checksums.txt":     checksums,
		"checksums.txt.sig": []byte("stub-sig\n"),
		"checksums.txt.pem": []byte("stub-pem\n"),
	}}
	f.sign()
	return f
}

func (f *caskFixture) sign() {
	f.assets["checksums.txt.sigstore.json"] = []byte(sha256hex(f.assets["checksums.txt"]) + "\n" + testIdentity + "\n")
}

func (f *caskFixture) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/"+testTag+"/")
		body, ok := f.assets[name]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type preflightResult struct {
	exit   int
	output string
}

// runPreflight executes the cask's preflight block against the fixture. cosignDir is the
// directory the harness's Formula["cosign"].opt_bin resolves to; "" means cosign is unavailable.
func runPreflight(t *testing.T, f *caskFixture, pinnedSha, cosignDir string) preflightResult {
	t.Helper()
	srv := f.serve(t)

	dir := t.TempDir()
	blockFile := filepath.Join(dir, "custom_block.rb")
	if err := os.WriteFile(blockFile, []byte(caskCustomBlock(t)), 0o644); err != nil {
		t.Fatal(err)
	}

	url := "https://github.com/partyline-sh/cli/releases/download/" + testTag + "/" + caskAsset
	cmd := exec.Command("ruby",
		filepath.Join(repoRootForTest(t), "cask_preflight_harness_test.rb"),
		blockFile, strings.TrimPrefix(testTag, "v"), url, pinnedSha)
	cmd.Env = append(os.Environ(),
		"FIXTURE_BASE="+srv.URL,
		"STUB_COSIGN_DIR="+cosignDir,
	)
	out, err := cmd.CombinedOutput()

	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if asExitError(err, &ee) {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("running the preflight harness: %v\n%s", err, out)
		}
	}
	return preflightResult{exit: exit, output: string(out)}
}

func requireRuby(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not available — the Homebrew cask preflight block cannot be exercised")
	}
}

// A genuine release whose signed checksums.txt covers the cask's pinned sha256.
func TestCaskPreflightAcceptsGenuineRelease(t *testing.T) {
	requireShell(t)
	requireRuby(t)
	cosignDir := t.TempDir()
	writeStubCosign(t, cosignDir)

	pinned := strings.Repeat("ab", 32)
	res := runPreflight(t, newCaskFixture(pinned), pinned, cosignDir)
	if res.exit != 0 {
		t.Fatalf("a genuine release should pass preflight, got exit %d:\n%s", res.exit, res.output)
	}
}

// checksums.txt edited after signing.
func TestCaskPreflightAbortsOnTamperedChecksums(t *testing.T) {
	requireShell(t)
	requireRuby(t)
	cosignDir := t.TempDir()
	writeStubCosign(t, cosignDir)

	pinned := strings.Repeat("ab", 32)
	f := newCaskFixture(pinned)
	f.assets["checksums.txt"] = append(f.assets["checksums.txt"], []byte("extra  evil.tar.gz\n")...) // not re-signed

	res := runPreflight(t, f, pinned, cosignDir)
	if res.exit == 0 {
		t.Fatalf("a tampered checksums.txt must abort preflight:\n%s", res.output)
	}
	if !strings.Contains(res.output, "SIGNATURE VERIFICATION FAILED") {
		t.Errorf("abort message should name the signature as what failed:\n%s", res.output)
	}
}

// A genuinely signed checksums.txt that simply does not cover what this cask would install —
// e.g. a valid signature for a different release grafted onto a swapped tap entry.
func TestCaskPreflightAbortsWhenPinnedShaNotSigned(t *testing.T) {
	requireShell(t)
	requireRuby(t)
	cosignDir := t.TempDir()
	writeStubCosign(t, cosignDir)

	f := newCaskFixture(strings.Repeat("ab", 32)) // signed checksums cover THIS sha...
	res := runPreflight(t, f, strings.Repeat("cd", 32), cosignDir)
	if res.exit == 0 {
		t.Fatalf("a pinned sha256 absent from the signed checksums must abort preflight:\n%s", res.output)
	}
	if !strings.Contains(res.output, "NOT listed in the signed checksums.txt") {
		t.Errorf("abort message should say the signature doesn't cover this artifact:\n%s", res.output)
	}
}

// The signature asset isn't published at all.
func TestCaskPreflightAbortsWhenSignatureAssetMissing(t *testing.T) {
	requireShell(t)
	requireRuby(t)
	cosignDir := t.TempDir()
	writeStubCosign(t, cosignDir)

	pinned := strings.Repeat("ab", 32)
	f := newCaskFixture(pinned)
	delete(f.assets, "checksums.txt.sigstore.json")

	res := runPreflight(t, f, pinned, cosignDir)
	if res.exit == 0 {
		t.Fatalf("a release with no signature must abort preflight:\n%s", res.output)
	}
	if !strings.Contains(res.output, "could not download checksums.txt.sigstore.json") {
		t.Errorf("abort message should name the missing asset:\n%s", res.output)
	}
}

// `sha256 :no_check` would make the whole chain vacuous — the cask would pin nothing for the
// signed checksums.txt to be tied to.
func TestCaskPreflightAbortsWhenCaskPinsNoChecksum(t *testing.T) {
	requireShell(t)
	requireRuby(t)
	cosignDir := t.TempDir()
	writeStubCosign(t, cosignDir)

	res := runPreflight(t, newCaskFixture(strings.Repeat("ab", 32)), "no_check", cosignDir)
	if res.exit == 0 {
		t.Fatalf("a cask that pins no sha256 must abort preflight:\n%s", res.output)
	}
	if !strings.Contains(res.output, "does not pin a sha256") {
		t.Errorf("abort message should say the cask pins no sha256:\n%s", res.output)
	}
}

// A `cosign` that approves everything must not be able to wave a release through the cask
// either — the preflight block carries the same negative control as install.sh.
func TestCaskPreflightAbortsWhenCosignRubberStamps(t *testing.T) {
	requireShell(t)
	requireRuby(t)

	stubDir := t.TempDir()
	rubberStamp := `#!/bin/sh
if [ "$1" = "verify-blob" ] && [ "$2" = "--help" ]; then
  printf -- '--bundle\n--new-bundle-format\n--certificate-identity\n'
  exit 0
fi
echo "Verified OK"
exit 0
`
	if err := os.WriteFile(filepath.Join(stubDir, "cosign"), []byte(rubberStamp), 0o755); err != nil {
		t.Fatal(err)
	}

	pinned := strings.Repeat("ab", 32)
	res := runPreflight(t, newCaskFixture(pinned), pinned, stubDir)
	if res.exit == 0 {
		t.Fatalf("a cosign that approves everything must abort preflight:\n%s", res.output)
	}
	if !strings.Contains(res.output, "ACCEPTED a deliberately altered checksums.txt") {
		t.Errorf("abort message should say the verifier failed its negative control:\n%s", res.output)
	}
}

// No usable cosign: explain and abort rather than install unverified binaries. (The cask also
// declares `depends_on formula: "cosign"`, which the harness asserts — so in practice brew
// installs it; this covers the case where it is present but broken.)
func TestCaskPreflightAbortsWhenCosignUnusable(t *testing.T) {
	requireShell(t)
	requireRuby(t)

	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "cosign"), []byte("#!/bin/sh\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pinned := strings.Repeat("ab", 32)
	res := runPreflight(t, newCaskFixture(pinned), pinned, broken)
	if res.exit == 0 {
		t.Fatalf("an unusable cosign must abort preflight:\n%s", res.output)
	}
	if !strings.Contains(res.output, "cosign is not usable") {
		t.Errorf("abort message should say cosign is unusable:\n%s", res.output)
	}
	if !strings.Contains(res.output, "Refusing to install unverified binaries") {
		t.Errorf("abort message should state the refusal:\n%s", res.output)
	}
}
