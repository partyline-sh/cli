package main

import (
	"context"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"partyline.sh/partyline/internal/api"
)

// nodemetrics.go — the machine half of fleet node health (the web has accepted `config.metrics` on
// the heartbeat all along; nothing ever sent any, so every card read blank).
//
// THREE RULES, in priority order:
//
//  1. NEVER fail or delay the heartbeat. The beat is the liveness signal; a metric that cannot be
//     read is OMITTED and the beat goes out unchanged. Everything here is best-effort, every shell
//     out is bounded by a hard timeout, and a hung command can only lose its own metric.
//  2. ABSENT, never zero. The server's allow-list drops a non-finite value and CLAMPS the rest, and
//     an absent key is how the card says "this node never reported memory" instead of "this node
//     has no memory". So unavailable → the key is not in the JSON at all (see api.NodeMetrics for
//     why some fields are pointers and some are not).
//  3. CHEAP. This runs every ~60s forever. No polling loops, no sleeping samplers: CPU on Linux is
//     a delta against the PREVIOUS beat (so the first beat after start simply omits it), which
//     costs one small read and gives a 60s average rather than a 200ms spike.
//
// Platform coverage (macOS + Linux; the fleet is mac laptops plus one Linux box):
//
//	                 linux                         darwin
//	cpu_pct          /proc/stat delta              `top -l 2 -n 0 -s 0` (last sample)
//	load1            /proc/loadavg                 `sysctl -n vm.loadavg`
//	cpu_cores        runtime.NumCPU()              runtime.NumCPU()
//	mem_*            /proc/meminfo                 `sysctl -n hw.memsize` + `vm_stat`
//	disk_*           /proc/mounts + statfs         getfsstat(2)
//	uptime_s         /proc/uptime                  `sysctl -n kern.boottime`
//
// The PARSERS all live in this file, with no build tag, so they are unit-testable on either OS —
// only the I/O that actually needs a platform lives in nodemetrics_{linux,darwin}.go.

// cmdTimeout bounds every external command. Generous enough for a loaded laptop, short enough that
// even if every one of them wedged the whole collection still finishes well inside a beat.
const cmdTimeout = 5 * time.Second

// collectNodeMetrics builds the heartbeat's health block. Returns nil when NOTHING could be read,
// so the `metrics` key is omitted entirely rather than shipping an empty object.
func collectNodeMetrics() *api.NodeMetrics {
	m := &api.NodeMetrics{}

	if n := runtime.NumCPU(); n > 0 {
		m.CPUCores = n
	}
	if v, ok := readLoad1(); ok {
		m.Load1 = &v
	}
	if v, ok := readCPUPct(); ok {
		m.CPUPct = &v
	}
	if totalMB, usedPct, ok := readMemory(); ok {
		m.MemTotalMB = totalMB
		m.MemUsedPct = &usedPct
	}
	if d, ok := tightestDisk(diskCandidates()); ok {
		m.DiskMount = d.Mount
		m.DiskUsedPct = &d.UsedPct
		m.DiskFreeGB = &d.FreeGB
	}
	if v, ok := readUptime(); ok {
		m.UptimeS = &v
	}

	if m.Empty() {
		return nil
	}
	return m
}

// runBounded runs a command that ships with the OS and returns its stdout. A timeout KILLS the
// child (exec.CommandContext sends SIGKILL), so a wedged `top` costs this one metric and nothing
// else. Errors are swallowed on purpose: a missing tool is an omitted metric, not a broken beat.
func runBounded(name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// ── load average ─────────────────────────────────────────────────────────────

// parseProcLoadavg reads the 1-minute figure from /proc/loadavg
// ("0.52 0.58 0.59 1/1234 5678").
func parseProcLoadavg(s string) (float64, bool) {
	f := strings.Fields(s)
	if len(f) < 1 {
		return 0, false
	}
	return parseNonNegFloat(f[0])
}

// parseSysctlLoadavg reads the 1-minute figure from `sysctl -n vm.loadavg`, whose output is a
// brace-wrapped triple: "{ 3.03 3.25 3.47 }".
func parseSysctlLoadavg(s string) (float64, bool) {
	f := strings.Fields(strings.NewReplacer("{", " ", "}", " ").Replace(s))
	if len(f) < 1 {
		return 0, false
	}
	return parseNonNegFloat(f[0])
}

// ── cpu ──────────────────────────────────────────────────────────────────────

// cpuSample is one reading of the kernel's cumulative CPU counters. Utilisation is only ever a
// DIFFERENCE between two of these, which is why the first beat after start reports no cpu_pct: we
// have one sample and no honest way to turn it into a percentage.
type cpuSample struct {
	total uint64
	idle  uint64
}

var cpuPrev struct {
	sync.Mutex
	have   bool
	sample cpuSample
}

// parseProcStat reads the aggregate "cpu" line of /proc/stat into cumulative total/idle jiffies.
// Field order is fixed by the kernel: user nice system idle iowait irq softirq steal guest...
// iowait counts as idle (the CPU was not executing anything), guest time is already included in
// user, so summing every field would double-count it — hence the explicit stop at steal.
func parseProcStat(s string) (cpuSample, bool) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var out cpuSample
		for i, v := range f[1:] {
			if i > 7 { // user..steal; guest/guest_nice are already inside user/nice
				break
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return cpuSample{}, false
			}
			out.total += n
			if i == 3 || i == 4 { // idle, iowait
				out.idle += n
			}
		}
		if out.total == 0 {
			return cpuSample{}, false
		}
		return out, true
	}
	return cpuSample{}, false
}

// cpuPctFromSamples turns two cumulative samples into busy-percent over the interval between them.
// A counter that went backwards or did not move (suspended laptop, clock weirdness, two reads in
// the same jiffy) yields no reading rather than a fabricated one.
func cpuPctFromSamples(prev, cur cpuSample) (float64, bool) {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return 0, false
	}
	dTotal := float64(cur.total - prev.total)
	dIdle := float64(cur.idle - prev.idle)
	if dIdle > dTotal {
		return 0, false
	}
	return clampPct((dTotal - dIdle) / dTotal * 100), true
}

// parseTopCPU reads busy-percent from macOS `top -l 2 -n 0 -s 0`, which prints one "CPU usage" line
// per sample. The FIRST is since-boot and useless; the LAST is the delta over the interval between
// the two samples, which is what we want. Line shape:
//
//	CPU usage: 7.44% user, 11.25% sys, 81.30% idle
func parseTopCPU(s string) (float64, bool) {
	idle, found := 0.0, false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "CPU usage:") {
			continue
		}
		for _, part := range strings.Split(strings.TrimPrefix(line, "CPU usage:"), ",") {
			part = strings.TrimSpace(part)
			if !strings.HasSuffix(part, "idle") {
				continue
			}
			v, ok := parseNonNegFloat(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(part, "idle")), "%"))
			if ok {
				idle, found = v, true
			}
		}
	}
	if !found || idle > 100 {
		return 0, false
	}
	return clampPct(100 - idle), true
}

// ── memory ───────────────────────────────────────────────────────────────────

// parseMeminfo reads total size + used-percent from /proc/meminfo. Values are in kB.
//
// "Used" is total minus MemAvailable — the kernel's own estimate of what a new workload could get,
// which is the only number that answers "can this box build". MemFree alone would call a healthy
// machine with a large page cache full. MemAvailable has been there since Linux 3.14; on anything
// older we fall back to free+buffers+cached. Swap is deliberately ignored: a machine with no swap
// at all parses exactly the same (there is nothing to read), which is why nothing here touches it.
func parseMeminfo(s string) (totalMB int64, usedPct float64, ok bool) {
	vals := map[string]uint64{}
	for _, line := range strings.Split(s, "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		vals[key] = n // kB (every size line in meminfo is kB)
	}
	total := vals["MemTotal"]
	if total == 0 {
		return 0, 0, false
	}
	avail, haveAvail := vals["MemAvailable"]
	if !haveAvail {
		avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	if avail > total {
		avail = total
	}
	used := total - avail
	return int64(total / 1024), clampPct(float64(used) / float64(total) * 100), true
}

// parseVMStat reads USED bytes from macOS `vm_stat`.
//
// The page size is read from the header ("page size of 16384 bytes") rather than assumed: it is
// 4096 on Intel and 16384 on Apple silicon, and hard-coding either silently mis-sizes half the
// fleet by 4x. Used = active + wired + compressor-occupied — free/inactive/speculative are all
// reclaimable, so counting them would call every warm machine full. Labels are matched EXACTLY
// (the key before the colon): "Pages tag-storage non-tag wired" is not wired memory.
func parseVMStat(s string) (usedBytes uint64, ok bool) {
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return 0, false
	}
	pageSize := uint64(0)
	if _, rest, found := strings.Cut(lines[0], "page size of "); found {
		if n, err := strconv.ParseUint(strings.Fields(rest)[0], 10, 64); err == nil {
			pageSize = n
		}
	}
	if pageSize == 0 {
		return 0, false
	}
	pages := map[string]uint64{}
	for _, line := range lines[1:] {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), "."), 10, 64)
		if err != nil {
			continue
		}
		pages[strings.TrimSpace(key)] = n
	}
	active, haveActive := pages["Pages active"]
	wired, haveWired := pages["Pages wired down"]
	if !haveActive || !haveWired {
		return 0, false
	}
	return (active + wired + pages["Pages occupied by compressor"]) * pageSize, true
}

// ── disk ─────────────────────────────────────────────────────────────────────

// diskStat is one mounted volume's headroom.
type diskStat struct {
	Mount   string
	UsedPct float64
	FreeGB  float64
}

// diskMinBytes ignores volumes too small to be where anyone builds. Both platforms are littered
// with them — macOS's ~550 MB xarts/iSCPreboot/Hardware volumes, Linux's /boot and /boot/efi — and
// a 95%-full 500 MB EFI partition is not a machine that cannot build. Without this floor those tiny
// partitions win the "tightest" contest on a perfectly healthy node and the card cries wolf.
const diskMinBytes = uint64(2) << 30

// systemOnlyMount reports mounts that exist but are none of a build's business.
//
// This is a macOS shape: an APFS container's volumes all report the CONTAINER's capacity and free
// space, so /System/Volumes/{VM,Preboot,Update} and /System/Volumes/Data come back with the same
// percentage to within a few thousand blocks of noise. Ranking them picks a winner at random and
// then names it — "/System/Volumes/Update, 66% full" is a true number attached to a volume no human
// recognises. Data is the one that actually holds home directories and checkouts, so it is the only
// one of Apple's managed volumes we keep. /Volumes/* (external and extra disks) is NOT filtered:
// those are real, they are where people put big checkouts, and disk_mount names them.
func systemOnlyMount(mount string) bool {
	const apple = "/System/Volumes/"
	return strings.HasPrefix(mount, apple) && mount != apple+"Data"
}

// diskStatFrom converts raw statfs numbers into a diskStat. Used-percent is measured against what
// this process could actually USE (bavail), not the raw block count: on ext4 the ~5% reserved for
// root is unavailable to a build, and counting it as free would say a wedged box has room.
func diskStatFrom(mount string, blocks, bfree, bavail, bsize uint64) (diskStat, bool) {
	if bsize == 0 || blocks == 0 || bfree > blocks || bavail > blocks || systemOnlyMount(mount) {
		return diskStat{}, false
	}
	size := blocks * bsize
	if size < diskMinBytes {
		return diskStat{}, false
	}
	used := blocks - bfree
	usable := used + bavail // what this process sees as the whole volume
	if usable == 0 {
		return diskStat{}, false
	}
	return diskStat{
		Mount:   mount,
		UsedPct: clampPct(float64(used) / float64(usable) * 100),
		FreeGB:  float64(bavail*bsize) / (1 << 30),
	}, true
}

// tightestDisk picks the volume with the LEAST headroom, not the root volume: a box whose /data is
// full cannot build, and reporting a roomy / would say the exact opposite. disk_mount then names
// which volume the numbers describe, so the card is never ambiguous about what it is showing.
// Ties break on mount path so the reported volume is stable beat to beat.
func tightestDisk(all []diskStat) (diskStat, bool) {
	if len(all) == 0 {
		return diskStat{}, false
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].UsedPct != all[j].UsedPct {
			return all[i].UsedPct > all[j].UsedPct
		}
		return all[i].Mount < all[j].Mount
	})
	return all[0], true
}

// mountEntry is one line of /proc/mounts.
type mountEntry struct {
	Device string
	Mount  string
	FSType string
	Opts   string
}

// realFSTypes is an ALLOW-list of filesystems that hold real, writable storage. An allow-list
// rather than a deny-list because the deny-list is unbounded and gets the important case wrong:
// every snap package is a squashfs mounted at exactly 100% full, forever, and a deny-list that
// forgets one would pin the fleet card at "disk full" on every Ubuntu box in the fleet.
var realFSTypes = map[string]bool{
	"apfs": true, "btrfs": true, "exfat": true, "ext2": true, "ext3": true, "ext4": true,
	"f2fs": true, "hfs": true, "hfsplus": true, "jfs": true, "ntfs": true, "ntfs3": true,
	"reiserfs": true, "vfat": true, "xfs": true, "zfs": true,
}

// parseProcMounts parses /proc/mounts, keeping only volumes worth reporting: a real filesystem
// (see realFSTypes), mounted read-write, and one entry per device — a bind mount reports the same
// numbers under a second name, and reporting it twice would just make the tightest-volume pick
// depend on which name sorted first. Malformed lines are skipped, never fatal.
func parseProcMounts(s string) []mountEntry {
	seen := map[string]bool{}
	var out []mountEntry
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !realFSTypes[f[2]] {
			continue
		}
		// Mount options are a comma list whose FIRST entry is ro or rw. A read-only volume cannot
		// be filled and is not a build blocker (this is also what drops macOS's sealed system
		// volume and every mounted disk image).
		if opt, _, _ := strings.Cut(f[3], ","); opt == "ro" {
			continue
		}
		// /proc/mounts escapes spaces and friends as octal; unescape so the mount name we report
		// is the real path.
		dev, mnt := unescapeMountField(f[0]), unescapeMountField(f[1])
		if seen[dev] {
			continue
		}
		seen[dev] = true
		out = append(out, mountEntry{Device: dev, Mount: mnt, FSType: f[2], Opts: f[3]})
	}
	return out
}

// unescapeMountField undoes the \040-style octal escaping the kernel applies to device and mount
// paths in /proc/mounts. An unterminated or invalid escape is left as-is rather than dropped.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ── uptime ───────────────────────────────────────────────────────────────────

// parseProcUptime reads seconds-since-boot from /proc/uptime ("12345.67 98765.43").
func parseProcUptime(s string) (int64, bool) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, false
	}
	v, ok := parseNonNegFloat(f[0])
	if !ok {
		return 0, false
	}
	return int64(v), true
}

// parseBoottime derives uptime from `sysctl -n kern.boottime`, which prints a struct timeval:
//
//	{ sec = 1786371583, usec = 835897 } Mon Aug 10 07:19:43 2026
//
// A boot time in the future (a machine whose clock just stepped) yields nothing rather than a
// negative uptime the server would clamp to 0 and the card would read as "just booted".
func parseBoottime(s string, now time.Time) (int64, bool) {
	_, rest, found := strings.Cut(s, "sec =")
	if !found {
		return 0, false
	}
	field, _, _ := strings.Cut(rest, ",")
	sec, err := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
	if err != nil || sec <= 0 {
		return 0, false
	}
	up := now.Unix() - sec
	if up < 0 {
		return 0, false
	}
	return up, true
}

// ── small shared helpers ─────────────────────────────────────────────────────

// parseNonNegFloat accepts only a finite, non-negative number. Every metric here is a magnitude, so
// a negative or NaN reading means the source was not what we thought it was — omit, never send.
func parseNonNegFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 || v != v { // NaN fails the comparison; ParseFloat rejects "inf" spellings only partly
		return 0, false
	}
	if v > 1e18 { // +Inf and absurd magnitudes alike
		return 0, false
	}
	return v, true
}

// clampPct keeps a percentage inside 0..100 locally too. The server clamps as well, but a value
// that arrives already sane is one the fleet card renders without depending on that.
func clampPct(v float64) float64 {
	if v != v {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
