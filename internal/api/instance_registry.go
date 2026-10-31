package api

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// instance_registry.go — the instance a machine belongs to, named by something that survives a move.
//
// THE BUG THIS EXISTS FOR. Credentials, device enrolment and the TLS pin were stored under
// ~/.partyline/envs/<host>/, so the hostname WAS the identity. Moving an install from
// https://192.168.1.170:8443 to https://partyline.example.com made every enrolled machine a
// stranger: fresh directory, no token, no device, and daemons retrying an endpoint that had stopped
// listening. Nothing on any client said why, and the fix was manual on every box.
//
// A hostname is one address an instance currently answers on, not the instance. So the client keeps
// a small local index from instance id → the directory that already holds that instance's files,
// and ConfigDir() resolves through it.
//
// NO FILES EVER MOVE. This is the part that makes the change safe to ship to running installs. A
// directory keeps whatever name it was created with; the registry just learns which instance it
// belongs to. An existing install adopts its own directory on the first probe, and the NEXT time
// that instance changes address the same directory is found by id. Nothing is renamed, so nothing
// is lost if an old binary, a backup script or a second machine reads the same tree.
//
// WHY NOT DERIVE IDENTITY FROM THE URL AT ALL. Because the reverse — minting an id into the
// container or a config file — breaks the case self-hosters actually hit: destroy the container,
// pull a new image, restore the volume. That id would be regenerated on every rebuild and orphan
// the fleet each time, which is strictly worse than keying on the hostname. The id lives in the
// instance's DATABASE (see the instance_identity migration), so it rides the restore that already
// defines "same instance" and a genuinely fresh install correctly gets a new one.

// registryPath sits at the ROOT of ~/.partyline, beside `instance` and outside ConfigDir() — the
// index that RESOLVES the config directory cannot live inside the directory it resolves.
func registryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".partyline", "instances.json")
}

// InstanceRecord is what the client remembers about one instance it has talked to.
type InstanceRecord struct {
	// Dir is the LEAF NAME under ~/.partyline/envs holding this instance's credentials. A name,
	// never a path: it is joined onto the envs root and sanitised on the way out, so a corrupted
	// or hand-edited registry cannot walk the config directory somewhere else.
	Dir string `json:"dir"`

	// BaseURL is the address this instance was last seen at — advisory, for display and for
	// telling an operator where their fleet went. Authorisation never reads it.
	BaseURL string `json:"base_url,omitempty"`
	Name    string `json:"name,omitempty"`
	SeenAt  string `json:"seen_at,omitempty"`
}

// instanceRegistry is the on-disk index. Hosts is the lookup ConfigDir() needs on every call —
// resolving a host to an id must never touch the network, so the last probe's answer is cached
// here and simply reused until a probe says otherwise.
type instanceRegistry struct {
	Instances map[string]InstanceRecord `json:"instances"`
	Hosts     map[string]string         `json:"hosts"` // host → instance id
}

func loadRegistry() instanceRegistry {
	reg := instanceRegistry{Instances: map[string]InstanceRecord{}, Hosts: map[string]string{}}
	b, err := os.ReadFile(registryPath())
	if err != nil {
		return reg
	}
	// A registry that will not parse is a cache, not a source of truth: fall back to
	// hostname-keyed directories (the historical behaviour) rather than failing a command.
	var on instanceRegistry
	if json.Unmarshal(b, &on) != nil {
		return reg
	}
	if on.Instances != nil {
		reg.Instances = on.Instances
	}
	if on.Hosts != nil {
		reg.Hosts = on.Hosts
	}
	return reg
}

func saveRegistry(reg instanceRegistry) error {
	b, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(registryPath()), 0o700); err != nil {
		return err
	}
	// Temp file + rename: the daemon and an interactive CLI both write this, and a half-written
	// index would send the next command to a directory with no token in it.
	tmp := registryPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, registryPath())
}

// hostOf is the host part of a base URL, flattened to something safe as a single path segment.
func hostOf(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	host := ""
	if err == nil {
		host = u.Host
	}
	if host == "" {
		return "custom"
	}
	return hostSafe.ReplaceAllString(host, "_")
}

// RememberInstance records that `base` is served by the instance with this id, and returns the
// directory that instance's credentials live in.
//
// THE MOVE IS HANDLED HERE, and it is the whole point: when the id is already known, the record's
// existing Dir is kept and the new host is simply pointed at it. The machine keeps the token and
// the device enrolment it already had, because they were never the hostname's to begin with.
//
// An empty id means the instance could not vouch for itself (an older build, or a database that
// was down during the probe). That is not an error and not a new instance — it leaves the registry
// untouched, so the caller keeps whatever mapping it already had.
func RememberInstance(base, id, name string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	host := hostOf(base)
	reg := loadRegistry()

	rec, known := reg.Instances[id]
	if !known || strings.TrimSpace(rec.Dir) == "" {
		// First sighting. Adopt the directory this host ALREADY uses, so an install that has been
		// running for months keeps its exact files and notices nothing. Only when that directory
		// belongs to a different instance — the same URL rebuilt against a fresh database — is a
		// distinct name needed, and then the id disambiguates it.
		rec.Dir = host
		if owner, taken := reg.Hosts[host]; taken && owner != id {
			rec.Dir = host + "-" + shortID(id)
		}
		for otherID, other := range reg.Instances {
			if otherID != id && other.Dir == rec.Dir {
				rec.Dir = host + "-" + shortID(id)
				break
			}
		}
	}
	rec.BaseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	rec.Name = strings.TrimSpace(name)
	rec.SeenAt = time.Now().UTC().Format(time.RFC3339)

	reg.Instances[id] = rec
	reg.Hosts[host] = id
	_ = saveRegistry(reg) // advisory index; a machine that cannot write it still works today
	return rec.Dir
}

func shortID(id string) string {
	id = hostSafe.ReplaceAllString(id, "")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// InstanceDirFor returns the config directory leaf for a base URL, or "" when this client has
// never confirmed an identity for that host. Pure local lookup — never dials.
func InstanceDirFor(base string) string {
	reg := loadRegistry()
	id := reg.Hosts[hostOf(base)]
	if id == "" {
		return ""
	}
	return strings.TrimSpace(reg.Instances[id].Dir)
}

// KnownInstances lists what this machine has been enrolled with, newest sighting first is NOT
// guaranteed — callers that care sort. Used by `ptln doctor` and the move notice to say where a
// fleet's credentials actually are.
func KnownInstances() map[string]InstanceRecord {
	return loadRegistry().Instances
}

// InstanceIDFor is the id this client last confirmed for a base URL, or "".
func InstanceIDFor(base string) string { return loadRegistry().Hosts[hostOf(base)] }

// HostsForInstance lists every address this machine has seen one instance answer on.
//
// Exists for the orphan sweep: the always-on service is named after the address it was installed
// for, so an instance that moves leaves its old unit behind, running and retrying an endpoint that
// no longer listens (observed: two launchd agents, one pointed at a dead host). Cleaning that up
// needs proof that the old unit belongs to the SAME instance — never "any partyline unit" — and
// this is that proof.
func HostsForInstance(id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	var out []string
	for host, owner := range loadRegistry().Hosts {
		if owner == id {
			out = append(out, host)
		}
	}
	return out
}

// ForgetHost drops one address from the index, after its service and directory have been dealt
// with. The instance record itself stays: it is still the same partyline, just no longer reachable
// at that address.
func ForgetHost(host string) {
	reg := loadRegistry()
	if _, ok := reg.Hosts[host]; !ok {
		return
	}
	delete(reg.Hosts, host)
	_ = saveRegistry(reg)
}
