package main

// Tests for web/public/install.sh — the curl|sh installer.
//
// The property under test is FAIL CLOSED: a release whose checksums.txt does not verify, an
// archive that does not match the (verified) checksums, a missing signature asset, or a missing
// cosign must all abort with a non-zero exit and leave NOTHING installed. A verification step
// that is performed and then ignored on error is the failure mode these tests exist to prevent.
//
// cosign itself is stubbed. Keyless verification needs Fulcio, Rekor and a live OIDC identity, so
// it cannot run in a unit test — and it is already exercised for real, against the published
// assets, by the "Verify the published signatures" step in .github/workflows/release.yml. What is
// unverified here and only here is the SCRIPT's control flow, so the stub is a real (toy)
// signature check rather than a rubber stamp: it binds a digest and a signer identity, so a
// tampered checksums.txt genuinely fails verification instead of failing by arrangement.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	testTag      = "v1.2.3"
	testIdentity = "https://github.com/partyline-sh/partyline/.github/workflows/release.yml@refs/tags/" + testTag
)

// minimalPath keeps the host's own cosign (and anything else in /opt/homebrew or ~/go/bin) off
// PATH so "cosign is not installed" can actually be simulated. The standard dirs are still there
// because the script legitimately needs curl, tar, shasum, install and mktemp.
const minimalPath = "/usr/bin:/bin:/usr/sbin:/sbin"

// fakeRelease is a release's assets, keyed by file name, served over HTTP.
type fakeRelease struct {
	assets map[string][]byte
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// tarballName mirrors install.sh's own os/arch detection.
func tarballName() string {
	return fmt.Sprintf("partyline_%s_%s_%s.tar.gz", strings.TrimPrefix(testTag, "v"), runtime.GOOS, runtime.GOARCH)
}

// makeTarball builds a .tar.gz holding a stand-in `partyline` binary that prints marker.
func makeTarball(t *testing.T, marker string) []byte {
	t.Helper()
	script := "#!/bin/sh\necho " + marker + "\n"

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "partyline", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newFakeRelease produces a coherent, correctly "signed" release: checksums.txt covers the
// tarball, and the bundle binds checksums.txt's digest to the expected signer identity.
func newFakeRelease(t *testing.T, marker string) *fakeRelease {
	t.Helper()
	tarball := makeTarball(t, marker)
	checksums := []byte(fmt.Sprintf("%s  %s\n%s  partyline_1.2.3_linux_amd64.deb\n",
		sha256hex(tarball), tarballName(), strings.Repeat("0", 64)))

	r := &fakeRelease{assets: map[string][]byte{
		tarballName():       tarball,
		"checksums.txt":     checksums,
		"checksums.txt.sig": []byte("stub-sig\n"),
		"checksums.txt.pem": []byte("stub-pem\n"),
	}}
	r.sign()
	return r
}

// sign (re)writes the stub Sigstore bundle over the CURRENT checksums.txt. Tests that tamper
// after signing deliberately do not call this — that is the attack being simulated.
func (r *fakeRelease) sign() {
	r.assets["checksums.txt.sigstore.json"] = []byte(sha256hex(r.assets["checksums.txt"]) + "\n" + testIdentity + "\n")
}

func (r *fakeRelease) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": %q}`, testTag)
	})
	mux.HandleFunc("/dl/"+testTag+"/", func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/dl/"+testTag+"/")
		body, ok := r.assets[name]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeStubCosign writes a cosign stand-in that verifies the toy signature described above.
func writeStubCosign(t *testing.T, dir string) string {
	t.Helper()
	sum := "shasum -a 256"
	if _, err := exec.LookPath("shasum"); err != nil {
		sum = "sha256sum"
	}
	script := `#!/bin/sh
if [ "$1" = "version" ]; then echo "stub cosign"; exit 0; fi
if [ "$1" = "verify-blob" ] && [ "$2" = "--help" ]; then
  printf -- '--bundle\n--new-bundle-format\n--certificate-identity\n--certificate-oidc-issuer\n'
  exit 0
fi
[ "$1" = "verify-blob" ] || { echo "stub cosign: unexpected invocation: $*" >&2; exit 2; }
shift
blob=$1; shift
bundle=""; identity=""
while [ $# -gt 0 ]; do
  case "$1" in
    --bundle) bundle=$2; shift 2 ;;
    --certificate-identity) identity=$2; shift 2 ;;
    --certificate-oidc-issuer) shift 2 ;;
    --new-bundle-format) shift ;;
    *) shift ;;
  esac
done
[ -n "$bundle" ] || { echo "stub cosign: no --bundle given" >&2; exit 2; }
want_digest=$(sed -n 1p "$bundle")
want_identity=$(sed -n 2p "$bundle")
got=$(` + sum + ` "$blob" | awk '{print $1}')
[ "$got" = "$want_digest" ] || { echo "stub cosign: blob digest does not match the signature" >&2; exit 1; }
[ "$identity" = "$want_identity" ] || { echo "stub cosign: signer identity mismatch" >&2; exit 1; }
echo "Verified OK"
`
	path := filepath.Join(dir, "cosign")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type runResult struct {
	exit    int
	output  string
	destDir string
}

// runInstaller executes install.sh against the fake release. cosignDir is prepended to PATH so
// the script finds the stub the way it finds any tool — the script has no "use this cosign"
// setting, deliberately, because that would be a documented bypass. "" means no cosign at all.
func runInstaller(t *testing.T, r *fakeRelease, cosignDir string) runResult {
	t.Helper()
	srv := r.serve(t)

	home := t.TempDir()
	dest := filepath.Join(t.TempDir(), "bin")

	path := minimalPath
	if cosignDir != "" {
		path = cosignDir + ":" + minimalPath
	}

	cmd := exec.Command("/bin/sh", installScriptPath(t))
	cmd.Env = []string{
		"PATH=" + path,
		"HOME=" + home,
		"PARTYLINE_INSTALL_API_URL=" + srv.URL + "/api/latest",
		"PARTYLINE_INSTALL_BASE_URL=" + srv.URL + "/dl",
		"PARTYLINE_INSTALL_DIR=" + dest,
	}
	out, err := cmd.CombinedOutput()

	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("running install.sh: %v\n%s", err, out)
		}
	}
	return runResult{exit: exit, output: string(out), destDir: dest}
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

// installScriptPath locates install.sh, skipping when it isn't there. scripts/mirror-cli.sh
// prunes web/ from the public partyline-sh/cli tree but keeps the root *.go files, so this test
// travels to a repo that has no installer to run — skip rather than fail there.
func installScriptPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRootForTest(t), "web", "public", "install.sh")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("web/public/install.sh is not present in this tree: %v", err)
	}
	return path
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

// assertNothingInstalled is the half of "fail closed" that matters most: aborting loudly while
// still having dropped a binary on disk would be no better than not checking.
func assertNothingInstalled(t *testing.T, res runResult) {
	t.Helper()
	entries, err := os.ReadDir(res.destDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("install aborted but wrote %v into the install dir; nothing should have been installed", names)
	}
}

func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is a POSIX shell script")
	}
	for _, tool := range []string{"/bin/sh"} {
		if _, err := os.Stat(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

// A genuine, correctly signed release installs and the binary runs.
func TestInstallScriptGenuineReleaseInstalls(t *testing.T) {
	requireShell(t)
	stubDir := t.TempDir()
	writeStubCosign(t, stubDir)
	r := newFakeRelease(t, "partyline-under-test")

	res := runInstaller(t, r, stubDir)
	if res.exit != 0 {
		t.Fatalf("genuine release should install, got exit %d:\n%s", res.exit, res.output)
	}
	if !strings.Contains(res.output, "signature verified") {
		t.Errorf("expected the installer to report signature verification:\n%s", res.output)
	}
	if !strings.Contains(res.output, "checksum verified") {
		t.Errorf("expected the installer to report checksum verification:\n%s", res.output)
	}

	installed := filepath.Join(res.destDir, "partyline")
	out, err := exec.Command(installed).Output()
	if err != nil {
		t.Fatalf("installed binary does not run: %v", err)
	}
	if strings.TrimSpace(string(out)) != "partyline-under-test" {
		t.Errorf("installed binary printed %q", string(out))
	}
	if _, err := os.Lstat(filepath.Join(res.destDir, "ptln")); err != nil {
		t.Errorf("the ptln alias was not created: %v", err)
	}
}

// checksums.txt altered after signing: the signature no longer covers it.
func TestInstallScriptAbortsOnTamperedChecksums(t *testing.T) {
	requireShell(t)
	stubDir := t.TempDir()
	writeStubCosign(t, stubDir)
	r := newFakeRelease(t, "genuine")

	evil := makeTarball(t, "pwned")
	r.assets[tarballName()] = evil
	// Rewrite checksums.txt to match the swapped archive but do NOT re-sign — exactly what an
	// attacker who can serve assets but cannot mint a partyline signature is limited to.
	r.assets["checksums.txt"] = []byte(fmt.Sprintf("%s  %s\n", sha256hex(evil), tarballName()))

	res := runInstaller(t, r, stubDir)
	if res.exit == 0 {
		t.Fatalf("a tampered checksums.txt must abort the install, got exit 0:\n%s", res.output)
	}
	if !strings.Contains(res.output, "SIGNATURE VERIFICATION FAILED") {
		t.Errorf("abort message should name the signature as what failed:\n%s", res.output)
	}
	if !strings.Contains(res.output, "cosign verify-blob") {
		t.Errorf("abort message should include the manual verification command:\n%s", res.output)
	}
	assertNothingInstalled(t, res)
}

// A validly signed checksums.txt, but the archive served does not match it.
func TestInstallScriptAbortsOnTamperedArchive(t *testing.T) {
	requireShell(t)
	stubDir := t.TempDir()
	writeStubCosign(t, stubDir)
	r := newFakeRelease(t, "genuine")
	r.assets[tarballName()] = makeTarball(t, "pwned") // checksums.txt still signed, now stale

	res := runInstaller(t, r, stubDir)
	if res.exit == 0 {
		t.Fatalf("a tampered archive must abort the install, got exit 0:\n%s", res.output)
	}
	if !strings.Contains(res.output, "CHECKSUM MISMATCH") {
		t.Errorf("abort message should name the checksum as what failed:\n%s", res.output)
	}
	assertNothingInstalled(t, res)
}

// A genuine signature from the wrong workflow/repo must not be accepted.
func TestInstallScriptAbortsOnWrongSignerIdentity(t *testing.T) {
	requireShell(t)
	stubDir := t.TempDir()
	writeStubCosign(t, stubDir)
	r := newFakeRelease(t, "genuine")
	r.assets["checksums.txt.sigstore.json"] = []byte(
		sha256hex(r.assets["checksums.txt"]) + "\nhttps://github.com/attacker/repo/.github/workflows/release.yml@refs/tags/" + testTag + "\n")

	res := runInstaller(t, r, stubDir)
	if res.exit == 0 {
		t.Fatalf("a signature from another identity must abort the install, got exit 0:\n%s", res.output)
	}
	if !strings.Contains(res.output, "SIGNATURE VERIFICATION FAILED") {
		t.Errorf("abort message should name the signature as what failed:\n%s", res.output)
	}
	assertNothingInstalled(t, res)
}

// The signature asset simply isn't published: still an abort, not a silent downgrade.
func TestInstallScriptAbortsWhenSignatureAssetMissing(t *testing.T) {
	requireShell(t)
	stubDir := t.TempDir()
	writeStubCosign(t, stubDir)
	r := newFakeRelease(t, "genuine")
	delete(r.assets, "checksums.txt.sigstore.json")

	res := runInstaller(t, r, stubDir)
	if res.exit == 0 {
		t.Fatalf("a release with no signature must abort the install, got exit 0:\n%s", res.output)
	}
	if !strings.Contains(res.output, "checksums.txt.sigstore.json") {
		t.Errorf("abort message should name the missing asset:\n%s", res.output)
	}
	assertNothingInstalled(t, res)
}

// No cosign on the machine: explain, don't proceed.
func TestInstallScriptAbortsWhenCosignMissing(t *testing.T) {
	requireShell(t)
	if path, err := exec.LookPath("cosign"); err == nil && strings.HasPrefix(path, "/usr/") {
		t.Skipf("cosign is installed at %s, inside the minimal PATH — cannot simulate its absence", path)
	}
	r := newFakeRelease(t, "genuine")

	res := runInstaller(t, r, "")
	if res.exit == 0 {
		t.Fatalf("a missing cosign must abort the install, got exit 0:\n%s", res.output)
	}
	if !strings.Contains(res.output, "cosign is not installed") {
		t.Errorf("abort message should say cosign is missing:\n%s", res.output)
	}
	if !strings.Contains(res.output, "brew install cosign") {
		t.Errorf("abort message should say how to get cosign:\n%s", res.output)
	}
	if !strings.Contains(res.output, "cosign verify-blob") {
		t.Errorf("abort message should include the manual verification command:\n%s", res.output)
	}
	assertNothingInstalled(t, res)
}

// A `cosign` that approves everything — a shim ahead of the real one on PATH, or a wrapper
// someone installed to "fix" a failing check — must not be able to wave a release through. The
// negative control in install.sh exists for exactly this, and this test is what keeps it honest.
func TestInstallScriptAbortsWhenCosignRubberStamps(t *testing.T) {
	requireShell(t)
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

	res := runInstaller(t, newFakeRelease(t, "genuine"), stubDir)
	if res.exit == 0 {
		t.Fatalf("a cosign that approves everything must abort the install, got exit 0:\n%s", res.output)
	}
	if !strings.Contains(res.output, "ACCEPTED a deliberately altered checksums.txt") {
		t.Errorf("abort message should say the verifier failed its negative control:\n%s", res.output)
	}
	assertNothingInstalled(t, res)
}

// install.sh must not offer a way to nominate the verifier or skip verification: a documented
// knob is a documented bypass, and it is the first thing anyone blocked by a real failure would
// reach for. cosign comes from PATH like any other tool and then has to prove itself.
func TestInstallScriptHasNoVerificationBypass(t *testing.T) {
	body, err := os.ReadFile(installScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, bypass := range []string{"PARTYLINE_COSIGN", "SKIP_VERIFY", "INSECURE", "NO_VERIFY"} {
		if strings.Contains(string(body), bypass) {
			t.Errorf("install.sh references %q — verification must not be redirectable or skippable by configuration", bypass)
		}
	}
}

// The installer must never strip com.apple.quarantine: that bypasses Gatekeeper on exactly the
// binaries a user is least able to inspect. See the comment block in install.sh.
func TestInstallScriptDoesNotStripQuarantine(t *testing.T) {
	body, err := os.ReadFile(installScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "xattr -d com.apple.quarantine") {
		t.Error("install.sh strips com.apple.quarantine — that disables Gatekeeper's check on the installed binaries")
	}
}
