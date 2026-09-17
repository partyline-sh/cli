package main

// autoadopt.go — a team project appears, and every machine that already has its repo picks it up
// by itself.
//
// THE FRICTION THIS REMOVES. Before this, one person ran `ptln project setup` and the project was
// buildable on exactly one box. Every OTHER machine needed its owner to notice the project existed,
// find the checkout, and run `ptln daemon add-project` by hand — four different flavors of the same
// chore, and the system already knew everything needed to do it: the project carries its repo_url,
// and the daemon already scans this machine's local clones. The human was the join. This loop IS
// the join.
//
// THE SECURITY LINE IT KEEPS. Nothing web-supplied resolves to a local path. The server names a
// repo (repo_url — DATA), and this machine COMPARES that against the origin remotes of clones it
// already scanned for itself (localrepos.go). A match registers the label against the machine's own
// path; no path ever leaves the box, and the server cannot cause a registration for a repo the
// machine does not already have. Registration-is-consent still holds, once, at the team level: a
// TEAM project is the owner saying "my team's agents may build this", and a machine whose owner is
// on that team adopting their own clone of that same repo is the grant working as stated — not a
// second grant needing a second ask. `ptln daemon autoadopt off` opts a box out entirely.
//
// PRIVATE projects never reach another user's daemon at all: the daemon-facing list is scoped
// server-side to visibility='team' or created-by-this-owner, mirroring the projects RLS policy.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"partyline.sh/partyline/internal/api"
)

// autoAdoptOffPath is the per-node OPT-OUT marker — note the inversion from provision.on /
// autoupdate.on: auto-adopt defaults ON, because making a team project buildable everywhere is the
// feature, and a default-off convenience is a convenience nobody has. Its existence = disabled.
func autoAdoptOffPath() string { return filepath.Join(daemonDir(), "autoadopt.off") }

func autoAdoptEnabled() bool {
	_, err := os.Stat(autoAdoptOffPath())
	return err != nil
}

func setAutoAdoptEnabled(on bool) error {
	p := autoAdoptOffPath()
	if !on {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		return os.WriteFile(p, []byte("off\n"), 0o600)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// canonicalCache is the daemon's local copy of the projects its owner can see, written after every
// refresh so `ptln state` (and through it the tray) can list projects WITHOUT a network call — the
// same contract as invites_cache.go and link.json: the daemon fetches, everything else reads disk.
type canonicalCache struct {
	FetchedAt string             `json:"fetched_at"`
	Projects  []canonicalProject `json:"projects"`
}

// canonicalProject is one project as the tray shows it: names and flags only, never a path.
type canonicalProject struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	DisplayName string `json:"display_name,omitempty"`
	Visibility  string `json:"visibility"`             // "team" | "private" (private ⇒ it is the owner's own)
	Mine        bool   `json:"mine,omitempty"`         // created by this machine's owner
	HasRepo     bool   `json:"has_repo,omitempty"`     // the project carries a repo_url (adoptable at all)
	AdoptedHere bool   `json:"adopted_here,omitempty"` // registered on THIS machine (buildable here)
}

func canonicalProjectsPath() string { return filepath.Join(daemonDir(), "canonical_projects.json") }

func readCanonicalCache() (canonicalCache, bool) {
	b, err := os.ReadFile(canonicalProjectsPath())
	if err != nil {
		return canonicalCache{}, false
	}
	var c canonicalCache
	if json.Unmarshal(b, &c) != nil {
		return canonicalCache{}, false
	}
	return c, true
}

func writeCanonicalCache(c canonicalCache) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := canonicalProjectsPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, canonicalProjectsPath()) // atomic: `ptln state` reads on its own schedule
}

// localRepoRemote pairs one local checkout with its origin remote — the comparison material.
type localRepoRemote struct {
	Path   string
	Remote string
}

// localRepoRemotes reads the origin remote of every repo the machine's own scan found. The scan is
// the SAME one the heartbeat advertises from (cached, linked-worktrees and managed clones excluded
// by construction), so auto-adopt can never touch a directory the machine would not itself offer.
func localRepoRemotes() []localRepoRemote {
	s := cachedRepoScan()
	out := make([]localRepoRemote, 0, len(s.paths))
	for _, p := range s.paths {
		if r := gitOriginURL(p); r != "" {
			out = append(out, localRepoRemote{Path: p, Remote: r})
		}
	}
	return out
}

// matchAdoptions is the pure core: which canonical projects should THIS machine register, given
// what it already advertises and which repos it has. A project is adopted when
//
//   - it carries a repo_url that names the same repo as a local clone's origin (sameRemote — the
//     ssh/https/.git spellings all count), and
//   - nothing on this machine advertises its label yet (an existing registration is NEVER
//     repointed — same rule as registerProjectHere, for the same reason: silently redirecting a
//     label sends the team's runs into a different checkout without the person who registered
//     the first one getting a say).
//
// A label that fails labelRe is skipped rather than registered: the registry's own shape rule
// applies no matter who minted the label.
func matchAdoptions(projects []api.CanonicalProject, registered map[string]bool, repos []localRepoRemote) []daemonProject {
	var out []daemonProject
	for _, p := range projects {
		if p.RepoURL == "" || registered[p.Label] || !labelRe.MatchString(p.Label) {
			continue
		}
		for _, r := range repos {
			if sameRemote(p.RepoURL, r.Remote) {
				out = append(out, daemonProject{Label: p.Label, Path: r.Path, Preset: "spec"})
				break
			}
		}
	}
	return out
}

// refreshCanonicalProjects is one tick of the loop: fetch the owner's projects, adopt what this
// machine can, and write the cache the tray reads. Returns the labels adopted THIS tick so the
// caller can say so. Fetch failure keeps the previous cache — a network blip must not blank the
// tray's project list.
func refreshCanonicalProjects(d daemonDevice) ([]string, error) {
	ps, err := api.FetchCanonicalProjects(d.Base, d.Token)
	if err != nil {
		return nil, err
	}

	var adopted []string
	reg := loadDaemonRegistry()
	if autoAdoptEnabled() {
		registered := make(map[string]bool, len(reg.Projects))
		for _, p := range reg.Projects {
			registered[p.Label] = true
		}
		adds := matchAdoptions(ps, registered, localRepoRemotes())
		if len(adds) > 0 {
			for _, a := range adds {
				upsertProject(&reg, a)
				adopted = append(adopted, a.Label)
			}
			if err := saveDaemonRegistry(reg); err != nil {
				return nil, err
			}
			invalidateLocalRepoCache()
			_ = mirrorProjects(d) // labels only; best-effort — the next heartbeat mirrors anyway
		}
	}

	c := canonicalCache{FetchedAt: time.Now().UTC().Format(time.RFC3339)}
	registered := make(map[string]bool, len(reg.Projects))
	for _, p := range reg.Projects {
		registered[p.Label] = true
	}
	for _, p := range ps {
		c.Projects = append(c.Projects, canonicalProject{
			ID:          p.ID,
			Label:       p.Label,
			DisplayName: p.DisplayName,
			Visibility:  p.Visibility,
			Mine:        p.Mine,
			HasRepo:     p.RepoURL != "",
			AdoptedHere: registered[p.Label],
		})
	}
	writeCanonicalCache(c)
	return adopted, nil
}

// autoAdoptInterval paces the loop. Far slower than the 60s heartbeat on purpose: a new project
// being buildable within ten minutes of a teammate creating it is the feature working; polling
// faster buys nothing but load. Creating a project on THIS machine registers it immediately
// through createProjectHere — this loop is for everyone else's machines.
const autoAdoptInterval = 10 * time.Minute

// daemonAutoAdopt is `ptln daemon autoadopt [on|off|status]`.
func daemonAutoAdopt(args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "on":
		if err := setAutoAdoptEnabled(true); err != nil {
			fatal(err)
		}
		fmt.Println("✓ auto-adopt ON — team projects whose repo this machine already has register themselves")
	case "off":
		if err := setAutoAdoptEnabled(false); err != nil {
			fatal(err)
		}
		fmt.Println("✓ auto-adopt OFF — projects on this machine are registered by hand only (ptln daemon add-project)")
	case "status":
		if autoAdoptEnabled() {
			fmt.Println("auto-adopt: on (default) — turn off with `ptln daemon autoadopt off`")
		} else {
			fmt.Println("auto-adopt: off — turn on with `ptln daemon autoadopt on`")
		}
	default:
		fatal(fmt.Errorf("usage: ptln daemon autoadopt [on|off|status]"))
	}
}
