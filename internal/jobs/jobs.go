// Package jobs is the shared core of scheduled and triggered agent runs: the job file format and
// its validation, the definition hash that approval is bound to, run states, and the Runner
// interface the CLI and the Jobs tab both call. Spec: ~/dev/compound/docs/SCHEDULED-AGENTS-SPEC.md.
//
// Nothing here starts a process, touches git or reads the network; executors and stores live
// elsewhere and implement Runner and Approvals.
package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode says whether a repo's jobs coordinate across machines. It is always written explicitly:
// "no other machine is visible right now" must never turn into local-only.
type Mode string

const (
	ModeLocalOnly   Mode = "local-only"
	ModeCoordinated Mode = "coordinated"
)

// Capability is something a job asks for. The job file only requests; each machine authorises.
type Capability string

const (
	CapReadRepo      Capability = "read-repo" // always granted: its fresh worktree, read-only
	CapWriteWorktree Capability = "write-worktree"
	CapNetwork       Capability = "network"
	CapFull          Capability = "full" // bypass sandbox and approvals; never a default
)

var knownCaps = map[Capability]bool{CapReadRepo: true, CapWriteWorktree: true, CapNetwork: true, CapFull: true}

// Coordination is the file's coordination block. Every field is part of the definition hash, so
// changing where or when jobs run revokes approval.
type Coordination struct {
	Mode   Mode   `yaml:"mode" json:"mode"`
	Remote string `yaml:"remote,omitempty" json:"remote,omitempty"`
	Ref    string `yaml:"ref,omitempty" json:"ref,omitempty"`
	TZ     string `yaml:"tz" json:"tz"`
}

// Trigger starts a job: a five-field cron schedule (in Coordination.TZ) or an event name.
type Trigger struct {
	Schedule string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	Event    string `yaml:"event,omitempty" json:"event,omitempty"`
}

// Limit bounds a run. Minutes is always enforced; it defaults to DefaultMinutes.
type Limit struct {
	Minutes int `yaml:"minutes,omitempty" json:"minutes,omitempty"`
}

// DefaultMinutes is a run's wall-clock limit when the job sets none.
const DefaultMinutes = 30

// Job is one entry in .partyline/jobs.yaml.
type Job struct {
	Name         string       `yaml:"-" json:"name"`
	Engine       string       `yaml:"engine" json:"engine"`
	Prompt       string       `yaml:"prompt" json:"prompt"` // repo-relative path
	Capabilities []Capability `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Limit        Limit        `yaml:"limit,omitempty" json:"limit"`
	On           Trigger      `yaml:"on" json:"on"`
	Catchup      string       `yaml:"catchup,omitempty" json:"catchup,omitempty"` // Go duration, e.g. "6h"
}

// File is a parsed .partyline/jobs.yaml.
type File struct {
	Coordination Coordination   `yaml:"coordination"`
	Jobs         map[string]Job `yaml:"jobs"`
}

var (
	jobName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	engines = map[string]bool{"claude": true, "codex": true}
	events  = map[string]bool{"loop.accepted": true, "loop.ready": true, "loop.pr_ready": true, "pr.merged": true, "session.idle": true, "chat.message": true}
	trigger = regexp.MustCompile(`^trigger:[a-z0-9][a-z0-9-]{0,40}$`)
)

// Parse reads and validates a jobs file. Every problem is reported, not just the first, so the
// form and `ptln jobs` can show them all at once.
func Parse(data []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a misspelled field is an error, not silently ignored
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return File{}, fmt.Errorf("jobs.yaml: %w", err)
	}
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	c := f.Coordination
	switch c.Mode {
	case ModeLocalOnly:
	case ModeCoordinated:
		if c.Remote == "" || c.Ref == "" {
			add("coordination: coordinated mode needs remote and ref")
		}
	case "":
		add("coordination.mode is required (local-only or coordinated); it is never inferred")
	default:
		add("coordination.mode %q: want local-only or coordinated", c.Mode)
	}
	if c.TZ == "" {
		add("coordination.tz is required, so every machine agrees what 07:00 means")
	} else if _, err := time.LoadLocation(c.TZ); err != nil {
		add("coordination.tz %q: %v", c.TZ, err)
	}

	names := make([]string, 0, len(f.Jobs))
	for n := range f.Jobs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		j := f.Jobs[n]
		j.Name = n
		if !jobName.MatchString(n) {
			add("job %q: names are lowercase letters, digits and dashes", n)
		}
		if !engines[j.Engine] {
			add("job %s: engine %q: want claude or codex", n, j.Engine)
		}
		if p := j.Prompt; p == "" || path.IsAbs(p) || strings.HasPrefix(path.Clean(p), "..") {
			add("job %s: prompt must be a path inside the repo", n)
		}
		for _, cap := range j.Capabilities {
			if !knownCaps[cap] {
				add("job %s: unknown capability %q", n, cap)
			}
		}
		if j.Limit.Minutes < 0 || j.Limit.Minutes > 24*60 {
			add("job %s: limit.minutes must be 1–1440", n)
		}
		if j.Limit.Minutes == 0 {
			j.Limit.Minutes = DefaultMinutes
		}
		switch {
		case j.On.Schedule != "" && j.On.Event != "":
			add("job %s: on: give a schedule or an event, not both", n)
		case j.On.Schedule != "":
			if err := checkCron(j.On.Schedule); err != nil {
				add("job %s: schedule %q: %v", n, j.On.Schedule, err)
			}
		case j.On.Event != "":
			if !events[j.On.Event] && !trigger.MatchString(j.On.Event) {
				add("job %s: event %q is not one partyline knows", n, j.On.Event)
			}
		}
		if j.Catchup != "" {
			if _, err := time.ParseDuration(j.Catchup); err != nil {
				add("job %s: catchup %q: %v", n, j.Catchup, err)
			}
		}
		f.Jobs[n] = j
	}
	if len(problems) > 0 {
		return File{}, fmt.Errorf("jobs.yaml:\n  %s", strings.Join(problems, "\n  "))
	}
	return f, nil
}

// cronBounds are the allowed values of each cron field: minute, hour, day, month, weekday (0–7, 7 = Sunday).
var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// checkCron validates five fields of *, numbers, ranges, lists and steps, each within its bounds;
// steps must be at least 1 and ranges must not run backwards. Evaluation belongs to the scheduler.
func checkCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("want 5 fields (minute hour day month weekday), got %d", len(fields))
	}
	names := [5]string{"minute", "hour", "day", "month", "weekday"}
	for i, field := range fields {
		lo, hi := cronBounds[i][0], cronBounds[i][1]
		for _, part := range strings.Split(field, ",") {
			base, step, hasStep := strings.Cut(part, "/")
			if hasStep {
				n, err := strconv.Atoi(step)
				if err != nil || n < 1 {
					return fmt.Errorf("%s %q: step must be a whole number of at least 1", names[i], part)
				}
			}
			if base == "*" {
				continue
			}
			from, to, isRange := strings.Cut(base, "-")
			a, errA := strconv.Atoi(from)
			z := a
			var errZ error
			if isRange {
				z, errZ = strconv.Atoi(to)
			}
			if errA != nil || errZ != nil {
				return fmt.Errorf("%s %q is not a number, range or *", names[i], part)
			}
			if a < lo || z > hi || a > z {
				return fmt.Errorf("%s %q: must be within %d–%d", names[i], part, lo, hi)
			}
		}
	}
	return nil
}

// RepoID is the canonical identity of a repo across machines: "host/owner/repo", lower-case
// host, no scheme, user or ".git". SSH, scp-style and HTTPS remotes for one repo agree.
func RepoID(remoteURL string) (string, error) {
	u := strings.TrimSpace(remoteURL)
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	switch {
	case strings.Contains(u, "://"):
		u = u[strings.Index(u, "://")+3:]
	case strings.Contains(u, ":") && !strings.Contains(strings.SplitN(u, ":", 2)[0], "/"):
		u = strings.Replace(u, ":", "/", 1) // scp-style git@host:owner/repo
	default:
		return "", fmt.Errorf("remote %q is not a URL partyline can identify", remoteURL)
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
		u = u[at+1:]
	}
	parts := strings.Split(u, "/")
	if len(parts) < 3 || parts[0] == "" {
		return "", fmt.Errorf("remote %q has no host/owner/repo", remoteURL)
	}
	host := strings.ToLower(parts[0])
	if i := strings.Index(host, ":"); i >= 0 { // drop a port
		host = host[:i]
	}
	return host + "/" + strings.Join(parts[1:], "/"), nil
}

// Definition is everything a run's behaviour depends on. Approval binds to its Hash.
type Definition struct {
	RepoID       string       `json:"repo"`
	Coordination Coordination `json:"coordination"`
	Job          Job          `json:"job"`
	PromptSHA256 string       `json:"prompt_sha256"`
}

// NewDefinition binds a job to its repo, its file's coordination block and its prompt contents.
func NewDefinition(repoID string, f File, name string, prompt []byte) (Definition, error) {
	j, ok := f.Jobs[name]
	if !ok {
		return Definition{}, fmt.Errorf("no job %q", name)
	}
	sum := sha256.Sum256(prompt)
	caps := append([]Capability(nil), j.Capabilities...)
	sort.Slice(caps, func(a, b int) bool { return caps[a] < caps[b] })
	j.Capabilities = caps
	return Definition{RepoID: repoID, Coordination: f.Coordination, Job: j, PromptSHA256: hex.EncodeToString(sum[:])}, nil
}

// Hash is the stable identity of this exact definition. Any change to the job entry, the prompt
// contents, the capabilities or the coordination block changes it.
func (d Definition) Hash() string {
	b, _ := json.Marshal(d) // struct field order is fixed, so the encoding is canonical
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ID is the job's identity across machines: repo + name. It does not change when the job is edited.
func (d Definition) ID() string { return d.RepoID + "#" + d.Job.Name }

// Has reports whether the definition requests a capability.
func (d Definition) Has(c Capability) bool {
	for _, x := range d.Job.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}
