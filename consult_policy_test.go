package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// The whole point of the split: auto-answering a read-only question must not require the privilege to
// run unattended WRITE work, and granting the weak one must not grant the strong one.
func TestConsultPolicyIsIndependentOfLaunchPolicy(t *testing.T) {
	cases := []struct {
		name          string
		p             daemonProject
		wantConsults  string
		wantAutoLaunc bool
	}{
		{"default project: answers questions, does not launch", daemonProject{}, "auto", false},
		{"ask-launch project still answers questions", daemonProject{Policy: "ask"}, "auto", false},
		{"full Auto implies auto-answer", daemonProject{Policy: "auto"}, "auto", true},
		{"explicit off, launch untouched", daemonProject{Consults: "ask"}, "ask", false},
		{"explicit off outranks full Auto", daemonProject{Policy: "auto", Consults: "ask"}, "ask", true},
		{"auto-answer does NOT grant auto-launch", daemonProject{Consults: "auto"}, "auto", false},
		{"a garbled value cannot silently disable it", daemonProject{Consults: "sometimes"}, "auto", false},
	}
	for _, c := range cases {
		if got := c.p.consultPolicy(); got != c.wantConsults {
			t.Errorf("%s: consultPolicy() = %q, want %q", c.name, got, c.wantConsults)
		}
		if got := c.p.launchPolicy() == "auto"; got != c.wantAutoLaunc {
			t.Errorf("%s: auto launch = %v, want %v", c.name, got, c.wantAutoLaunc)
		}
	}
}

// autoAnswerConsults must resolve against THIS machine's registry and nothing else. A label we don't
// advertise is never auto-answerable, which is what stops an unknown label from taking the no-human
// path (the decline branch upstream is the first wall; this is the second).
func TestAutoAnswerConsultsIsLocalOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{
		{Label: "web", Path: "/tmp/web"},
		{Label: "api", Path: "/tmp/api", Consults: "ask"},
	}}); err != nil {
		t.Fatal(err)
	}
	if !autoAnswerConsults("web") {
		t.Fatal("a registered project with no override should auto-answer")
	}
	if autoAnswerConsults("api") {
		t.Fatal("an explicit `consults ask` project must wait for the owner")
	}
	if autoAnswerConsults("not-mine") {
		t.Fatal("a label this machine doesn't advertise must never auto-answer")
	}
}

// ---- the machine-wide off switch ------------------------------------------

// THE PRECEDENCE INVARIANT. A global switch a per-project setting could override is not a safety
// switch: the operator who turns auto-answer off for the box must not have to audit every project.
func TestGlobalConsultOffBeatsPerProjectAuto(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{
		{Label: "web", Path: "/tmp/web", Consults: "auto"},    // explicitly opted in
		{Label: "full", Path: "/tmp/full", Policy: "auto"},    // full unattended-write Auto
		{Label: "quiet", Path: "/tmp/quiet", Consults: "ask"}, // already off
	}}); err != nil {
		t.Fatal(err)
	}
	if !autoAnswerConsults("web") {
		t.Fatal("baseline: an explicit per-project auto should auto-answer with no global setting")
	}
	if err := setGlobalConsultPolicy("ask"); err != nil {
		t.Fatal(err)
	}
	for _, lbl := range []string{"web", "full", "quiet"} {
		if autoAnswerConsults(lbl) {
			t.Fatalf("%s: machine-wide OFF must outrank the project's setting", lbl)
		}
		if v, _ := decideConsult(lbl, nil); v != consultVerdictQueue {
			t.Fatalf("%s: machine-wide OFF must QUEUE for the owner, got %v", lbl, v)
		}
	}
	// And back: global auto is only permission to consult the project's own setting, never a grant.
	if err := setGlobalConsultPolicy("auto"); err != nil {
		t.Fatal(err)
	}
	if !autoAnswerConsults("web") {
		t.Fatal("global auto should restore the project's own decision")
	}
	if autoAnswerConsults("quiet") {
		t.Fatal("global auto must NOT override a project's explicit ask — the switch only denies")
	}
}

// No env value can defeat a persisted OFF: policy is decided before the spend cap is even consulted.
func TestGlobalConsultOffIgnoresEnvCaps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConsultAutoDaily, "9999")
	t.Setenv(envConsultAutoDailyTotal, "9999")
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{{Label: "web", Path: "/tmp/web"}}}); err != nil {
		t.Fatal(err)
	}
	if err := setGlobalConsultPolicy("ask"); err != nil {
		t.Fatal(err)
	}
	if v, _ := decideConsult("web", nil); v != consultVerdictQueue {
		t.Fatalf("a generous env cap must not override the persisted OFF, got %v", v)
	}
}

// The switch must be re-read per decision, not cached at startup: the common install is an always-on
// service, and needing to restart it to stop this would make the switch useless in the moment it
// matters. This flips the file underneath a live process and expects the very next call to obey.
func TestGlobalConsultPolicyIsRereadWithoutRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{{Label: "web", Path: "/tmp/web"}}}); err != nil {
		t.Fatal(err)
	}
	if !autoAnswerConsults("web") { // warm any cache a future refactor might add
		t.Fatal("default should auto-answer")
	}
	if err := setGlobalConsultPolicy("ask"); err != nil {
		t.Fatal(err)
	}
	if autoAnswerConsults("web") {
		t.Fatal("the flip must be honoured on the next decision, with no restart")
	}
	if err := setGlobalConsultPolicy("auto"); err != nil {
		t.Fatal(err)
	}
	if !autoAnswerConsults("web") {
		t.Fatal("flipping back must be honoured live too")
	}
}

// Absent file = today's shipped behaviour (ON). Anything else = OFF. A truncated write, a garbled
// value, or a file we cannot read must FAIL CLOSED — a safety switch defeated by a partial write is
// not a safety switch.
func TestGlobalConsultPolicyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if got := globalConsultPolicyAt(filepath.Join(dir, "absent.mode")); got != "auto" {
		t.Fatalf("no file must preserve the shipped default, got %q", got)
	}
	for _, body := range []string{"", "\n", "au", "ask", "off", "AUTO", "auto extra", "{\"mode\":\"auto\"}"} {
		p := filepath.Join(dir, "m.mode")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := globalConsultPolicyAt(p); got != "ask" {
			t.Fatalf("body %q must resolve to ask (fail closed), got %q", body, got)
		}
	}
	// Only the exact value written by the CLI re-enables it.
	p := filepath.Join(dir, "ok.mode")
	if err := setGlobalConsultPolicyAt(p, "auto"); err != nil {
		t.Fatal(err)
	}
	if got := globalConsultPolicyAt(p); got != "auto" {
		t.Fatalf("an explicitly written auto must read back as auto, got %q", got)
	}
	// The file holds an operator decision about who may spend this machine's tokens: 0600, like the
	// budget ledger and device.json.
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	// Anything but "auto" is persisted as the off state.
	if err := setGlobalConsultPolicyAt(p, "sometimes"); err != nil {
		t.Fatal(err)
	}
	if got := globalConsultPolicyAt(p); got != "ask" {
		t.Fatalf("a garbled mode must be stored as off, got %q", got)
	}
	// An unreadable file is OFF, not ON.
	unreadable := filepath.Join(dir, "locked.mode")
	if err := os.WriteFile(unreadable, []byte("auto\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(unreadable, 0o600) }()
	if os.Geteuid() != 0 { // root can read anything; the assertion is meaningless there
		if got := globalConsultPolicyAt(unreadable); got != "ask" {
			t.Fatalf("an unreadable switch must fail closed to ask, got %q", got)
		}
	}
}

// ---- the cost bound -------------------------------------------------------

func TestConsultBudgetCapsPerProjectThenFallsBack(t *testing.T) {
	t.Setenv(envConsultAutoDaily, "2")
	t.Setenv(envConsultAutoDailyTotal, "99")
	path := filepath.Join(t.TempDir(), "consult-budget.json")
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 2; i++ {
		if ok, why := claimConsultAutoAnswerAt(path, "web", nil, now); !ok {
			t.Fatalf("claim %d should fit: %s", i, why)
		}
	}
	ok, why := claimConsultAutoAnswerAt(path, "web", nil, now)
	if ok {
		t.Fatal("the third claim must not fit")
	}
	if why == "" {
		t.Fatal("a refusal must say which cap was hit — the console line explains why a question queued")
	}
	// Another project has its own allowance; one label's abuse must not silence the machine.
	if ok, why := claimConsultAutoAnswerAt(path, "api", nil, now); !ok {
		t.Fatalf("a different project should still have allowance: %s", why)
	}
}

func TestConsultBudgetMachineWideCap(t *testing.T) {
	t.Setenv(envConsultAutoDaily, "99")
	t.Setenv(envConsultAutoDailyTotal, "2")
	path := filepath.Join(t.TempDir(), "consult-budget.json")
	now := time.Now()
	// Spread across labels: a per-project cap alone would let a fan-out multiply the spend.
	for _, lbl := range []string{"a", "b"} {
		if ok, _ := claimConsultAutoAnswerAt(path, lbl, nil, now); !ok {
			t.Fatalf("%s should fit under the machine total", lbl)
		}
	}
	if ok, _ := claimConsultAutoAnswerAt(path, "c", nil, now); ok {
		t.Fatal("a third label must be refused once the machine-wide total is spent")
	}
}

func TestConsultBudgetRollsOverDailyAndSurvivesGarbage(t *testing.T) {
	t.Setenv(envConsultAutoDaily, "1")
	path := filepath.Join(t.TempDir(), "consult-budget.json")
	day1 := time.Date(2026, 7, 25, 23, 0, 0, 0, time.UTC)
	if ok, _ := claimConsultAutoAnswerAt(path, "web", nil, day1); !ok {
		t.Fatal("first claim of the day should fit")
	}
	if ok, _ := claimConsultAutoAnswerAt(path, "web", nil, day1); ok {
		t.Fatal("cap of 1 means the second claim on the same day is refused")
	}
	if ok, _ := claimConsultAutoAnswerAt(path, "web", nil, day1.Add(2*time.Hour)); !ok {
		t.Fatal("a new day is a new allowance")
	}
	// The ledger is a cache: an unreadable one grants a fresh day rather than failing the daemon.
	b := loadConsultBudgetAt(filepath.Join(t.TempDir(), "absent.json"), day1)
	if b.Total != 0 || b.Day != "2026-07-25" {
		t.Fatalf("a missing ledger should read as an empty day, got %+v", b)
	}
}

// A typo in an override must not uncap the control, and 0 must mean "never auto-answer" (a real
// setting, not an unset one).
func TestConsultBudgetOverrideParsing(t *testing.T) {
	t.Setenv(envConsultAutoDaily, "not-a-number")
	if got := consultAutoDailyCap(nil); got != defaultConsultAutoDaily {
		t.Fatalf("a garbled override should fall back to the default, got %d", got)
	}
	t.Setenv(envConsultAutoDaily, "0")
	if got := consultAutoDailyCap(nil); got != 0 {
		t.Fatalf("0 must be honoured as a cap of zero, got %d", got)
	}
	path := filepath.Join(t.TempDir(), "b.json")
	if ok, _ := claimConsultAutoAnswerAt(path, "web", nil, time.Now()); ok {
		t.Fatal("a cap of 0 must send every consult to the human queue")
	}
}

// The PROJECT-WIDE cap (projects.consult_auto_daily, delivered on the consult event) sets the
// allowance, and the machine clamps it: min(project, ceiling), with anything missing or nonsensical
// falling back to today's default. The clamp only ever runs DOWNWARD.
func TestConsultCapClampsProjectSetting(t *testing.T) {
	n := func(v int) *int { return &v }
	for _, tc := range []struct {
		name    string
		project *int
		want    int
	}{
		{"below the default is honoured as-is", n(5), 5},
		{"above the machine ceiling is clamped to it", n(5_000), hardConsultAutoDaily},
		{"exactly at the ceiling passes", n(hardConsultAutoDaily), hardConsultAutoDaily},
		{"zero means never auto-answer", n(0), 0},
		{"missing falls back to the built-in default", nil, defaultConsultAutoDaily},
		{"negative (garbled) falls back, never uncaps", n(-1), defaultConsultAutoDaily},
	} {
		if got := consultAutoDailyCap(tc.project); got != tc.want {
			t.Fatalf("%s: want %d, got %d", tc.name, tc.want, got)
		}
	}

	// The env override now BOUNDS the project setting rather than being overridden by it: a machine can
	// always tighten below what the project asks for.
	t.Setenv(envConsultAutoDaily, "3")
	if got := consultAutoDailyCap(n(100)); got != 3 {
		t.Fatalf("the env ceiling must clamp a generous project setting, got %d", got)
	}
	if got := consultAutoDailyCap(n(1)); got != 1 {
		t.Fatalf("a project setting below the env ceiling stands, got %d", got)
	}
	if got := consultAutoDailyCap(nil); got != 3 {
		t.Fatalf("no project setting → the env value is the fallback, got %d", got)
	}

	// A project cap of 0 must actually stop the auto-answer, not just shrink it.
	path := filepath.Join(t.TempDir(), "b.json")
	if ok, why := claimConsultAutoAnswerAt(path, "web", n(0), time.Now()); ok || why == "" {
		t.Fatalf("a project cap of 0 must queue every consult, with a reason (ok=%v why=%q)", ok, why)
	}
	// And the machine total must not silently swallow a project that legitimately asks for more than 48.
	if got := consultAutoDailyTotalCap(120); got != 120 {
		t.Fatalf("the machine total should rise to meet a larger project cap, got %d", got)
	}
	if got := consultAutoDailyTotalCap(10_000); got != hardConsultAutoDailyTotal {
		t.Fatalf("the machine total must stay under its hard ceiling, got %d", got)
	}
}

// decideConsult's gating arguments are local state only: the project cap it now takes is SPEND data,
// so there is still no parameter through which an asker's request could steer the verdict.
func TestDecideConsultUsesOnlyLocalState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envConsultAutoDaily, "1")
	t.Setenv(envConsultAutoDailyTotal, "99")
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{
		{Label: "web", Path: "/tmp/web"},
		{Label: "api", Path: "/tmp/api", Consults: "ask"},
	}}); err != nil {
		t.Fatal(err)
	}

	if v, _ := decideConsult("nope", nil); v != consultVerdictDecline {
		t.Fatalf("a label this machine doesn't advertise must decline, got %v", v)
	}
	if v, _ := decideConsult("api", nil); v != consultVerdictQueue {
		t.Fatalf("`consults ask` must queue for the owner, got %v", v)
	}
	if v, _ := decideConsult("web", nil); v != consultVerdictAutoAnswer {
		t.Fatalf("a default project with budget must auto-answer, got %v", v)
	}
	// The cap of 1 is now spent. Over budget is a QUEUE, never a decline and never a drop — the human
	// can still say yes, which is the whole point of falling back rather than failing.
	v, why := decideConsult("web", nil)
	if v != consultVerdictQueue {
		t.Fatalf("over budget must fall back to the owner's queue, got %v", v)
	}
	if why == "" {
		t.Fatal("the fallback must explain which cap was hit")
	}
}

// consultAsker: the approval prompt used to read `"partyline" asks: …` — the %q was the PROJECT
// label, so the project appeared to be asking and the human was nowhere. Approving an anonymous
// question is a poor decision to put in front of anyone.
func TestConsultAskerNamesTheHuman(t *testing.T) {
	if got := consultAsker(api.ConsultEvent{FromHandle: "matthew"}); got != "@matthew" {
		t.Fatalf("want @matthew, got %q", got)
	}
	// An older control plane sends no handle. Say so plainly rather than printing a blank or, worse,
	// falling back to the project label and recreating the bug.
	for _, missing := range []string{"", "   "} {
		if got := consultAsker(api.ConsultEvent{FromHandle: missing}); got != "someone on your team" {
			t.Fatalf("missing handle should degrade honestly, got %q", got)
		}
	}
}
