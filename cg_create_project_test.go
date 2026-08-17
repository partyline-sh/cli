package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"partyline.sh/partyline/internal/api"
)

// create_project is a WRITE against three places at once — the org, the working tree, and this
// machine's registry — so every test here is about a way it could quietly do the wrong one: make a
// second project for a repo that already has one, key a project on a path instead of a remote, or
// register a directory for unattended builds without saying so.

// fakeControlPlane stands in for the control plane and COUNTS the writes, which is the only way to
// prove "exactly one project" rather than "at least one".
type fakeControlPlane struct {
	mu sync.Mutex

	// what the org already has
	projects []api.Project
	// what /threads/resolve matches this remote to ("" = not a project yet)
	resolvedLabel  string
	resolvedThread string

	creates    int // POST /api/v1/projects
	threadMade int // POST /threads/resolve with create:true
	repoURLSet map[string]string
}

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()
	f := &fakeControlPlane{repoURLSet: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/threads/resolve":
			var body struct {
				Remote string `json:"remote"`
				Name   string `json:"name"`
				Create bool   `json:"create"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if f.resolvedThread != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"thread_id": f.resolvedThread, "title": body.Name, "project_label": f.resolvedLabel, "created": false,
				})
				return
			}
			if !body.Create {
				_ = json.NewEncoder(w).Encode(map[string]any{"thread_id": nil, "project_label": nil, "created": false})
				return
			}
			f.threadMade++
			f.resolvedThread = "th-made-1"
			_ = json.NewEncoder(w).Encode(map[string]any{"thread_id": f.resolvedThread, "title": body.Name, "created": true})
		case r.Method == "GET" && r.URL.Path == "/api/v1/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": f.projects})
		case r.Method == "POST" && r.URL.Path == "/api/v1/projects":
			var body struct {
				Label string `json:"label"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.creates++
			p := api.Project{ID: "p-new-1", Label: body.Label}
			f.projects = append(f.projects, p)
			_ = json.NewEncoder(w).Encode(map[string]any{"project": p})
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/api/v1/projects/"):
			var body struct {
				RepoURL string `json:"repo_url"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.repoURLSet[strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")] = body.RepoURL
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PARTYLINE_API", srv.URL)
	return f
}

// signedIn plants a token where api.LoadToken looks (scoped to PARTYLINE_API, so api.ConfigDir()
// rather than a hard-coded ~/.partyline).
func signedIn(t *testing.T) {
	t.Helper()
	cfg := api.ConfigDir()
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "token"), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gitRepo makes a real repo (the code shells out to git — a fake would test the fake).
func gitRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	if origin != "" {
		if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
			t.Fatalf("git remote add: %v %s", err, out)
		}
	}
	return dir
}

func TestCreateProjectSetsUpAFreshRepoExactlyOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeControlPlane(t)
	signedIn(t)
	repo := gitRepo(t, "git@github.com:acme/widgets.git")
	t.Chdir(repo)

	set, msg, isErr := createProjectHere(api.New(), "")
	if isErr {
		t.Fatalf("refused a perfectly good repo: %s", msg)
	}
	if f.creates != 1 {
		t.Errorf("POST /projects happened %d times, want exactly 1", f.creates)
	}
	if f.threadMade != 1 {
		t.Errorf("threads created = %d, want exactly 1", f.threadMade)
	}
	if got := f.repoURLSet["p-new-1"]; got != "git@github.com:acme/widgets.git" {
		t.Errorf("project was not stamped with its repo (got %q) — nothing could resolve back to it", got)
	}

	// The pin — what makes a teammate who pulls land in the SAME thread.
	b, err := os.ReadFile(filepath.Join(repo, ".partyline.json"))
	if err != nil {
		t.Fatalf("no .partyline.json written: %v", err)
	}
	if !strings.Contains(string(b), set.Thread) {
		t.Errorf(".partyline.json does not pin the thread: %s", b)
	}

	// The registry entry — without it promote_work_item refuses, because nothing advertises the label.
	p := projectByLabel(loadDaemonRegistry(), set.Label)
	if p == nil {
		t.Fatalf("label %q is not registered on this machine", set.Label)
	}
	if !pathsEqual(p.Path, repo) {
		t.Errorf("registered path = %s, want %s", p.Path, repo)
	}
	if set.Label != filepath.Base(repo) {
		t.Errorf("label = %q, want the repo dir name %q", set.Label, filepath.Base(repo))
	}

	// Registration is a consent boundary: it must be stated, with the undo.
	if !strings.Contains(strings.ToLower(msg), "unattended") {
		t.Errorf("summary never says the directory can be built in UNATTENDED:\n%s", msg)
	}
	if !strings.Contains(msg, "ptln daemon remove-project") {
		t.Errorf("summary never says how to undo the registration:\n%s", msg)
	}
}

// THE DUPLICATE GUARD. A repo the org already has a project for must be ADOPTED, never doubled — two
// labels for one repo split the backlog, the runs and the board.
func TestCreateProjectRebindsInsteadOfDuplicating(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeControlPlane(t)
	f.resolvedLabel, f.resolvedThread = "widgets", "th-existing"
	f.projects = []api.Project{{ID: "p-old", Label: "widgets", RepoURL: "git@github.com:acme/widgets.git"}}
	signedIn(t)
	repo := gitRepo(t, "git@github.com:acme/widgets.git")
	t.Chdir(repo)

	set, msg, isErr := createProjectHere(api.New(), "")
	if isErr {
		t.Fatalf("refused an already-registered repo instead of adopting it: %s", msg)
	}
	if f.creates != 0 {
		t.Errorf("created %d project(s) for a repo that already had one — that is the duplicate", f.creates)
	}
	if f.threadMade != 0 {
		t.Errorf("created %d thread(s) for a repo that already had one", f.threadMade)
	}
	if set.Label != "widgets" {
		t.Errorf("label = %q, want the EXISTING project's label %q", set.Label, "widgets")
	}
	if set.Thread != "th-existing" {
		t.Errorf("thread = %q, want the existing thread", set.Thread)
	}
	if set.MadeProject {
		t.Error("reported the project as newly created when it already existed")
	}
	if !strings.Contains(msg, "Adopted the existing project") {
		t.Errorf("summary claims a creation that did not happen:\n%s", msg)
	}
}

// A project under the same LABEL but belonging to a DIFFERENT repo must refuse rather than take the
// name — stealing it would point the team's runs at the wrong checkout.
func TestCreateProjectRefusesToStealAnotherReposLabel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeControlPlane(t)
	signedIn(t)
	repo := gitRepo(t, "git@github.com:acme/widgets.git")
	f.projects = []api.Project{{ID: "p-old", Label: filepath.Base(repo), RepoURL: "git@github.com:other/thing.git"}}
	t.Chdir(repo)

	_, msg, isErr := createProjectHere(api.New(), "")
	if !isErr {
		t.Fatalf("took a label that names someone else's repo: %s", msg)
	}
	if f.creates != 0 {
		t.Errorf("wrote %d project(s) on a path that should have refused", f.creates)
	}
	if !strings.Contains(msg, "different repo") {
		t.Errorf("refusal does not name the reason:\n%s", msg)
	}
	// The refusal has to be actionable: which repo that project belongs to, and which one is here.
	if !strings.Contains(msg, "git@github.com:other/thing.git") || !strings.Contains(msg, "git@github.com:acme/widgets.git") {
		t.Errorf("refusal names neither repo, so there is nothing to act on:\n%s", msg)
	}
}

// THE COLLISION CHECK COMPARES REPOS, not "does it have one". The server declining to match is NOT
// proof of a different repo — /threads/resolve answers about the THREAD, so a project of ours whose
// plan thread is missing comes back as no match. Refusing then would decline to set a repo up
// against ITS OWN project, quoting its own remote back as somebody else's.
func TestCreateProjectAdoptsALabelledProjectThatIsThisSameRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeControlPlane(t)
	signedIn(t)
	repo := gitRepo(t, "git@github.com:acme/widgets.git")
	// Same repo, a different legal spelling of it — and the control plane matched nothing.
	f.projects = []api.Project{{ID: "p-old", Label: filepath.Base(repo), RepoURL: "https://github.com/acme/widgets"}}
	t.Chdir(repo)

	set, msg, isErr := createProjectHere(api.New(), "")
	if isErr {
		t.Fatalf("refused this repo against its OWN project: %s", msg)
	}
	if f.creates != 0 {
		t.Errorf("created %d project(s) when one for this repo already existed", f.creates)
	}
	if set.ProjectID != "p-old" {
		t.Errorf("project id = %q, want the existing p-old", set.ProjectID)
	}
	if set.MadeProject {
		t.Error("reported a creation that did not happen")
	}
	if _, rewritten := f.repoURLSet["p-old"]; rewritten {
		t.Error("rewrote the project's repo_url to our spelling of the same repo — pointless churn on a shared row")
	}
}

// A directory that is not a repo has no stable identity to key a project on. Refuse, and say that.
func TestCreateProjectRefusesANonGitDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeControlPlane(t)
	signedIn(t)
	t.Chdir(t.TempDir())

	_, msg, isErr := createProjectHere(api.New(), "")
	if !isErr {
		t.Fatal("made a project for a directory that is not a git repository")
	}
	if !strings.Contains(msg, "not a git repository") {
		t.Errorf("refusal does not give the reason:\n%s", msg)
	}
	if f.creates != 0 {
		t.Errorf("wrote %d project(s) before refusing", f.creates)
	}
}

// No origin remote: a LOCAL PATH IS NOT AN IDENTITY — the same path on another machine is a
// different repo. Refuse with that reason rather than keying a project on it.
func TestCreateProjectRefusesARepoWithNoOriginRemote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeControlPlane(t)
	signedIn(t)
	t.Chdir(gitRepo(t, ""))

	_, msg, isErr := createProjectHere(api.New(), "")
	if !isErr {
		t.Fatal("made a project for a repo with no origin remote")
	}
	if !strings.Contains(msg, "origin") {
		t.Errorf("refusal does not name the missing remote:\n%s", msg)
	}
	if f.creates != 0 {
		t.Errorf("wrote %d project(s) before refusing", f.creates)
	}
}

func TestCreateProjectRefusesWhenNotLoggedIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	newFakeControlPlane(t) // no token planted
	t.Chdir(gitRepo(t, "git@github.com:acme/widgets.git"))

	_, msg, isErr := createProjectHere(api.New(), "")
	if !isErr {
		t.Fatal("proceeded with no account token")
	}
	if !strings.Contains(msg, "ptln login") {
		t.Errorf("refusal does not point at `ptln login`:\n%s", msg)
	}
}

// A label already advertised for ANOTHER directory is not repointed silently: the summary must say
// the setup is PARTIAL and that promote will refuse. A half-finished setup that reads as complete is
// worse than a clear partial.
func TestCreateProjectReportsAPartialSetupWhenRegistrationFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	newFakeControlPlane(t)
	signedIn(t)
	repo := gitRepo(t, "git@github.com:acme/widgets.git")
	other := t.TempDir()
	if err := saveDaemonRegistry(daemonRegistry{Projects: []daemonProject{{Label: filepath.Base(repo), Path: other}}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	set, msg, isErr := createProjectHere(api.New(), "")
	if isErr {
		t.Fatalf("unexpected refusal: %s", msg)
	}
	if set.Registered {
		t.Fatal("claimed to register a label this machine already points elsewhere")
	}
	if p := projectByLabel(loadDaemonRegistry(), set.Label); p == nil || !pathsEqual(p.Path, other) {
		t.Error("the existing registration was repointed — runs for that label would move checkout")
	}
	if !strings.Contains(msg, "PARTIAL") || !strings.Contains(msg, "promote_work_item") {
		t.Errorf("summary does not say the setup is partial and what will refuse:\n%s", msg)
	}
}

// THE IN-PROCESS REBIND (step 3). planning_open must work in the SAME session, with no relaunch —
// the tool exists because the user asked mid-conversation.
func TestCreateProjectRebindsTheLiveSessionThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	newFakeControlPlane(t)
	signedIn(t)
	t.Chdir(gitRepo(t, "git@github.com:acme/widgets.git"))

	s := &cgServer{c: api.New()}
	if s.resolveThread(); s.thread != "" {
		t.Fatalf("precondition: session must start threadless, got %q", s.thread)
	}
	var out strings.Builder
	params, _ := json.Marshal(map[string]any{"name": "create_project", "arguments": map[string]any{}})
	s.handleCall(json.NewEncoder(&out), rpcReq{ID: json.RawMessage(`1`), Params: params})

	if strings.Contains(out.String(), `"isError":true`) {
		t.Fatalf("create_project returned an error: %s", out.String())
	}
	if s.thread == "" {
		t.Fatal("s.thread is still empty — planning_open would refuse in this session, defeating the tool")
	}
}

// The #586 case the ticket is about: `remember` auto-linked a THREAD for this repo, but nothing ever
// made a project. create_project must create the project and KEEP that thread — a second thread
// would strand every fact already recorded in the first one.
func TestCreateProjectKeepsAnExistingThreadWhenOnlyTheProjectIsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newFakeControlPlane(t)
	signedIn(t)
	repo := gitRepo(t, "git@github.com:acme/widgets.git")
	if err := os.WriteFile(filepath.Join(repo, ".partyline.json"), []byte(`{"thread":"th-from-remember"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	set, msg, isErr := createProjectHere(api.New(), "widgets")
	if isErr {
		t.Fatalf("unexpected refusal: %s", msg)
	}
	if f.creates != 1 {
		t.Errorf("created %d project(s), want exactly 1 — the project is the missing half", f.creates)
	}
	if f.threadMade != 0 {
		t.Errorf("created %d thread(s) — the repo already had one, and its facts live there", f.threadMade)
	}
	if set.Thread != "th-from-remember" {
		t.Errorf("thread = %q, want the pinned one", set.Thread)
	}
}

// The tool has to be ADVERTISED, or nothing can call it.
func TestCreateProjectToolIsRegistered(t *testing.T) {
	for _, d := range cgToolDefs {
		if d["name"] == "create_project" {
			return
		}
	}
	t.Fatal("create_project is not in cgToolDefs")
}
