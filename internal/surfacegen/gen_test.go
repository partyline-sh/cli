package surfacegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../.."

// A generated artifact feeds a diff-based drift gate. If generation were not deterministic the
// gate would fire on every run and be turned off within a week.
func TestGenerationIsDeterministic(t *testing.T) {
	a, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("file count differs between runs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Path != b[i].Path {
			t.Fatalf("file order differs: %s vs %s", a[i].Path, b[i].Path)
		}
		if string(a[i].Body) != string(b[i].Body) {
			t.Errorf("%s differs between two runs of the generator", a[i].Path)
		}
	}
}

// Every artifact must announce that it is generated. A reader who has never heard of this package
// still needs to know not to edit the file, and to know the one command that regenerates it.
func TestEveryArtifactCarriesTheBanner(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		body := string(f.Body)
		// llms.txt is consumed by language models as prose; a do-not-edit banner would be noise
		// in the one place the audience is a model. It is covered by the drift check instead.
		if strings.HasPrefix(f.Path, "web/public/llms") {
			continue
		}
		if !strings.Contains(body, "GENERATED — DO NOT EDIT") {
			t.Errorf("%s has no do-not-edit banner", f.Path)
		}
		if !strings.Contains(body, "make surface-gen") {
			t.Errorf("%s does not say how to regenerate it", f.Path)
		}
	}
}

// The drift gate itself. If Check cannot notice a hand edit, `make surface-check` is decoration.
func TestCheckDetectsAHandEdit(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no artifacts declared")
	}
	target := filepath.Join(repoRoot, filepath.FromSlash(files[0].Path))
	original, err := os.ReadFile(target)
	if err != nil {
		t.Skipf("%s not generated yet — run `make surface-gen`", files[0].Path)
	}
	t.Cleanup(func() { _ = os.WriteFile(target, original, 0o644) })

	if stale, _ := Check(repoRoot); len(stale) != 0 {
		t.Fatalf("tree is already stale before the mutation: %v — run `make surface-gen`", stale)
	}
	if err := os.WriteFile(target, append(original, []byte("\n// a human edited this\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	stale, err := Check(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0] != files[0].Path {
		t.Errorf("Check() = %v, want exactly [%s] — a hand edit went unnoticed", stale, files[0].Path)
	}
}

// The llms files exist to tell a model what partyline IS. The versions on disk before this package
// described a three-feature product and had not been touched since 5 July. Assert the generated
// ones actually mention the capabilities that make up most of the product now, so a future edit to
// the prose constants cannot quietly regress them.
func TestLLMsFilesDescribeTheCurrentProduct(t *testing.T) {
	files, err := Files(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, "web/public/llms") {
			continue
		}
		body := strings.ToLower(string(f.Body))
		for _, want := range []string{"crank", "worktree", "verify gate", "context threads", "daemon"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s never mentions %q", f.Path, want)
			}
		}
		// The support matrix is the specific place overclaiming is tempting and harmful.
		if !strings.Contains(body, "claude code only") {
			t.Errorf("%s does not state the Context Threads engine limitation", f.Path)
		}
	}
}
