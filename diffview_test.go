package main

import (
	"strings"
	"testing"
)

// The diff model is what the reviewer's eyes land on at the moment of the accept decision, so its
// rules get pinned without a repo: what counts as a change, how a rename reads, where a huge file
// gets cut, and that navigation lands on file boundaries.

const sampleDiff = `diff --git a/internal/api/client.go b/internal/api/client.go
index 111..222 100644
--- a/internal/api/client.go
+++ b/internal/api/client.go
@@ -10,6 +10,7 @@ import (
 	"net/http"
+	"time"
 )
@@ -40,3 +41,2 @@ func New() {
-	old := 1
-	older := 2
+	replacement := 3
diff --git a/retry.go b/retry.go
new file mode 100644
--- /dev/null
+++ b/retry.go
@@ -0,0 +1,2 @@
+package main
+func retry() {}
`

func TestParseCountsAddsAndDelsPerFile(t *testing.T) {
	files := parseUnifiedDiff(sampleDiff)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Path != "internal/api/client.go" || files[0].Adds != 2 || files[0].Dels != 2 {
		t.Fatalf("file 0: %+v", files[0])
	}
	if files[1].Path != "retry.go" || files[1].Adds != 2 || files[1].Dels != 0 {
		t.Fatalf("file 1: %+v", files[1])
	}
}

func TestParseReadsARename(t *testing.T) {
	files := parseUnifiedDiff("diff --git a/old.go b/new.go\nsimilarity index 90%\nrename from old.go\nrename to new.go\n")
	if len(files) != 1 || files[0].Path != "new.go" || files[0].OldPath != "old.go" {
		t.Fatalf("got %+v", files)
	}
}

func TestParseMarksBinary(t *testing.T) {
	files := parseUnifiedDiff("diff --git a/icon.png b/icon.png\nBinary files a/icon.png and b/icon.png differ\n")
	if len(files) != 1 || !files[0].Binary {
		t.Fatalf("got %+v", files)
	}
	doc := buildDiffDoc(files)
	if !strings.Contains(strings.Join(doc.Lines, "\n"), "binary file") {
		t.Fatal("a binary file must say so rather than render nothing")
	}
}

// Garbage in must not panic or error — the reviewer's alternative is no diff at all.
func TestParseToleratesGarbage(t *testing.T) {
	for _, s := range []string{"", "not a diff", "@@ floating hunk @@\n+x\n"} {
		_ = parseUnifiedDiff(s) // must not panic
	}
}

func TestHugeFileIsElidedWithACount(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.lock b/big.lock\n--- a/big.lock\n+++ b/big.lock\n@@ -0,0 +1,3000 @@\n")
	for i := 0; i < 3000; i++ {
		b.WriteString("+line\n")
	}
	doc := buildDiffDoc(parseUnifiedDiff(b.String()))
	joined := strings.Join(doc.Lines, "\n")
	if !strings.Contains(joined, "more lines in this file") {
		t.Fatal("a huge file must be elided with a count, not rendered in full")
	}
	if len(doc.Lines) > diffLineCap+10 {
		t.Fatalf("the document must be capped; got %d lines", len(doc.Lines))
	}
}

func TestFileNavigationLandsOnBoundaries(t *testing.T) {
	doc := buildDiffDoc(parseUnifiedDiff(sampleDiff))
	if len(doc.FileStart) != 2 {
		t.Fatalf("expected 2 file starts, got %v", doc.FileStart)
	}
	if got := doc.nextFile(0); got != doc.FileStart[1] {
		t.Fatalf("next from top should land on file 2's header; got %d want %d", got, doc.FileStart[1])
	}
	if got := doc.prevFile(doc.FileStart[1] + 1); got != doc.FileStart[1] {
		t.Fatalf("prev from inside file 2 should land on its header; got %d", got)
	}
	if got := doc.prevFile(doc.FileStart[1]); got != doc.FileStart[0] {
		t.Fatalf("prev from file 2's header should land on file 1; got %d", got)
	}
	if doc.fileAt(doc.FileStart[1]+1) != 1 {
		t.Fatal("fileAt should attribute lines to the file they fall in")
	}
}

func TestEmptyDiffSaysSo(t *testing.T) {
	doc := buildDiffDoc(nil)
	if len(doc.Lines) != 1 || !strings.Contains(doc.Lines[0], "no changes") {
		t.Fatalf("an empty diff must say so; got %v", doc.Lines)
	}
}

func TestStatLineTotals(t *testing.T) {
	got := diffStatLine(parseUnifiedDiff(sampleDiff))
	if !strings.Contains(got, "2 files") || !strings.Contains(got, "+4") || !strings.Contains(got, "−2") {
		t.Fatalf("got %q", got)
	}
}
