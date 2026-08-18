package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"partyline.sh/partyline/internal/api"
)

// nodemetrics_test.go — the parsers, as pure functions over REAL samples from both platforms. The
// point is never "this machine has RAM" (it does, and that proves nothing); it is that every shape
// these files come in — a different page size, a machine with no swap, a truncated file, a mount
// table full of squashfs — produces either a correct number or NO number, and never a wrong one.

func TestParseProcLoadavg(t *testing.T) {
	if v, ok := parseProcLoadavg("0.52 0.58 0.59 1/1234 5678\n"); !ok || v != 0.52 {
		t.Fatalf("got %v %v", v, ok)
	}
	// An idle box really is 0.00 — a valid reading, not a missing one.
	if v, ok := parseProcLoadavg("0.00 0.01 0.05 1/220 900\n"); !ok || v != 0 {
		t.Fatalf("idle load: got %v %v", v, ok)
	}
	for _, bad := range []string{"", "\n", "banana 1 2", "-1.0 0 0"} {
		if _, ok := parseProcLoadavg(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestParseSysctlLoadavg(t *testing.T) {
	if v, ok := parseSysctlLoadavg("{ 3.03 3.25 3.47 }\n"); !ok || v != 3.03 {
		t.Fatalf("got %v %v", v, ok)
	}
	if v, ok := parseSysctlLoadavg("{ 0.00 0.00 0.00 }\n"); !ok || v != 0 {
		t.Fatalf("idle: got %v %v", v, ok)
	}
	for _, bad := range []string{"", "{ }", "{ nope nope nope }"} {
		if _, ok := parseSysctlLoadavg(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// procStat is a real /proc/stat head; the per-core lines and the trailing counters must not confuse
// the aggregate read.
const procStat = `cpu  1000 20 300 8000 100 0 30 0 0 0
cpu0 500 10 150 4000 50 0 15 0 0 0
cpu1 500 10 150 4000 50 0 15 0 0 0
intr 12345 0 0
ctxt 987654
btime 1786371583
`

func TestParseProcStat(t *testing.T) {
	s, ok := parseProcStat(procStat)
	if !ok {
		t.Fatal("not parsed")
	}
	// user+nice+system+idle+iowait+irq+softirq+steal, with idle+iowait counted as idle.
	if s.total != 1000+20+300+8000+100+0+30+0 || s.idle != 8100 {
		t.Fatalf("got %+v", s)
	}
	for _, bad := range []string{"", "cpu\n", "cpu  1 2\n", "cpu  a b c d e\n", "cpu  0 0 0 0 0\n"} {
		if _, ok := parseProcStat(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestCPUPctFromSamples(t *testing.T) {
	prev := cpuSample{total: 1000, idle: 800}
	// 100 more jiffies, 25 of them idle → 75% busy.
	if v, ok := cpuPctFromSamples(prev, cpuSample{total: 1100, idle: 825}); !ok || v != 75 {
		t.Fatalf("got %v %v", v, ok)
	}
	// A fully idle interval is 0%, and 0% must be a READING (the collector sends it), not a gap.
	if v, ok := cpuPctFromSamples(prev, cpuSample{total: 1100, idle: 900}); !ok || v != 0 {
		t.Fatalf("idle interval: got %v %v", v, ok)
	}
	// Counters that stalled or went backwards (suspend/resume, clock step) yield NO reading.
	for _, cur := range []cpuSample{
		{total: 1000, idle: 800}, // no movement
		{total: 900, idle: 700},  // total went backwards
		{total: 1100, idle: 700}, // idle went backwards
		{total: 1010, idle: 900}, // idle grew more than total
	} {
		if _, ok := cpuPctFromSamples(prev, cur); ok {
			t.Fatalf("accepted %+v", cur)
		}
	}
}

// topOut is real `top -l 2 -n 0 -s 0` output: the FIRST sample is since-boot and must be ignored in
// favour of the last.
const topOut = `Processes: 700 total, 3 running, 697 sleeping, 4116 threads
2026/08/11 09:20:01
Load Avg: 3.03, 3.25, 3.47
CPU usage: 12.50% user, 6.25% sys, 81.25% idle
SharedLibs: 600M resident, 90M data, 40M linkedit.

Processes: 700 total, 2 running, 698 sleeping, 4116 threads
2026/08/11 09:20:02
Load Avg: 3.03, 3.25, 3.47
CPU usage: 9.23% user, 9.23% sys, 71.54% idle
`

func TestParseTopCPU(t *testing.T) {
	v, ok := parseTopCPU(topOut)
	if !ok || math.Abs(v-28.46) > 0.001 {
		t.Fatalf("got %v %v (want the LAST sample, 100-71.54)", v, ok)
	}
	if v, ok := parseTopCPU("CPU usage: 0.00% user, 0.00% sys, 100.00% idle\n"); !ok || v != 0 {
		t.Fatalf("fully idle: got %v %v", v, ok)
	}
	for _, bad := range []string{
		"",
		"Processes: 700 total\n",
		"CPU usage: 9.23% user, 9.23% sys\n",    // no idle field at all
		"CPU usage: 1% user, 1% sys, x% idle\n", // unparseable idle
		"CPU usage: 0% user, 0% sys, 140% idle\n",
	} {
		if _, ok := parseTopCPU(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// meminfoNoSwap is a real /proc/meminfo from a box with NO SWAP CONFIGURED — the shape that most
// often surprises a parser that assumes swap lines exist.
const meminfoNoSwap = `MemTotal:       16316912 kB
MemFree:         1234567 kB
MemAvailable:    8158456 kB
Buffers:          214000 kB
Cached:          6000000 kB
SwapCached:            0 kB
SwapTotal:             0 kB
SwapFree:              0 kB
Dirty:               128 kB
`

// meminfoOld predates MemAvailable (Linux < 3.14): free+buffers+cached is the fallback.
const meminfoOld = `MemTotal:        1000000 kB
MemFree:          100000 kB
Buffers:           50000 kB
Cached:           350000 kB
`

func TestParseMeminfo(t *testing.T) {
	total, used, ok := parseMeminfo(meminfoNoSwap)
	if !ok || total != 15934 { // 16316912 kB / 1024
		t.Fatalf("total: got %v %v", total, ok)
	}
	if math.Abs(used-50.0) > 0.01 { // MemAvailable is exactly half of MemTotal here
		t.Fatalf("used: got %v", used)
	}

	total, used, ok = parseMeminfo(meminfoOld)
	if !ok || total != 976 || math.Abs(used-50.0) > 0.01 {
		t.Fatalf("no MemAvailable: got %v %v %v", total, used, ok)
	}

	// MemAvailable can exceed MemTotal on some kernels/containers; that must read as 0% used, not
	// as a negative percentage.
	_, used, ok = parseMeminfo("MemTotal: 1000 kB\nMemAvailable: 2000 kB\n")
	if !ok || used != 0 {
		t.Fatalf("over-available: got %v %v", used, ok)
	}

	for _, bad := range []string{"", "garbage\n", "MemTotal: banana kB\n", "MemFree: 100 kB\n"} {
		if _, _, ok := parseMeminfo(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// vmStat16k is real Apple-silicon `vm_stat` (16 KiB pages).
const vmStat16k = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                    36722.
Pages active:                                 400000.
Pages inactive:                               370704.
Pages speculative:                             27978.
Pages throttled:                                   0.
Pages wired down:                             600000.
Pages purgeable:                                1120.
"Translation faults":                     1246341712.
Pages stored in compressor:                  2197979.
Pages occupied by compressor:                 500000.
Pages tag-storage non-tag wired:                  94.
`

// vmStat4k is the SAME machine shape with an Intel 4 KiB page size — the field that must never be
// assumed, since assuming one silently mis-sizes the other architecture by 4x.
const vmStat4k = `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                                    36722.
Pages active:                                 400000.
Pages wired down:                             600000.
Pages occupied by compressor:                 500000.
`

func TestParseVMStat(t *testing.T) {
	const pages = 400000 + 600000 + 500000
	if used, ok := parseVMStat(vmStat16k); !ok || used != pages*16384 {
		t.Fatalf("16k: got %v %v", used, ok)
	}
	if used, ok := parseVMStat(vmStat4k); !ok || used != pages*4096 {
		t.Fatalf("4k: got %v %v", used, ok)
	}
	// "Pages tag-storage non-tag wired" is NOT wired memory; exact-label matching is what keeps it out.
	if used, _ := parseVMStat(vmStat16k); used != pages*16384 {
		t.Fatalf("tag-storage leaked into the total: %v", used)
	}
	// No compressor line (older macOS): active+wired still parses.
	if used, ok := parseVMStat("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages active: 10.\nPages wired down: 5.\n"); !ok || used != 15*4096 {
		t.Fatalf("no compressor: got %v %v", used, ok)
	}
	for _, bad := range []string{
		"",
		"Mach Virtual Memory Statistics:\nPages active: 1.\nPages wired down: 1.\n",    // no page size
		"Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 1.\n", // no active/wired
		"Mach Virtual Memory Statistics: (page size of 0 bytes)\nPages active: 1.\nPages wired down: 1.\n",
	} {
		if _, ok := parseVMStat(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestParseProcUptime(t *testing.T) {
	if v, ok := parseProcUptime("350735.47 234388.90\n"); !ok || v != 350735 {
		t.Fatalf("got %v %v", v, ok)
	}
	for _, bad := range []string{"", "\n", "nope nope"} {
		if _, ok := parseProcUptime(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestParseBoottime(t *testing.T) {
	now := time.Unix(1786371583+3600, 0)
	const real = "{ sec = 1786371583, usec = 835897 } Mon Aug 10 07:19:43 2026\n"
	if v, ok := parseBoottime(real, now); !ok || v != 3600 {
		t.Fatalf("got %v %v", v, ok)
	}
	// Some releases print it without spaces around '='.
	if v, ok := parseBoottime("{ sec = 1786371583, usec = 0 }\n", now); !ok || v != 3600 {
		t.Fatalf("terse: got %v %v", v, ok)
	}
	// A boot time in the FUTURE (clock step) is not "just booted" — it is no reading.
	if _, ok := parseBoottime(real, time.Unix(1786371583-10, 0)); ok {
		t.Fatal("accepted a future boot time")
	}
	for _, bad := range []string{"", "{ }", "{ sec = banana, usec = 0 }", "{ sec = -5, usec = 0 }"} {
		if _, ok := parseBoottime(bad, now); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

// procMounts is a real Ubuntu /proc/mounts: pseudo filesystems, a fistful of snap squashfs mounts
// (every one of them permanently 100% full), a bind mount of the data volume, and one escaped path.
const procMounts = `sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
udev /dev devtmpfs rw,nosuid,relatime,size=8123456k 0 0
tmpfs /run tmpfs rw,nosuid,nodev,noexec,relatime,size=1633520k 0 0
/dev/nvme0n1p2 / ext4 rw,relatime,errors=remount-ro 0 0
/dev/loop0 /snap/core22/1122 squashfs ro,nodev,relatime 0 0
/dev/loop1 /snap/snapd/20290 squashfs ro,nodev,relatime 0 0
/dev/nvme0n1p1 /boot/efi vfat rw,relatime,fmask=0077 0 0
/dev/nvme1n1 /data ext4 rw,relatime 0 0
/dev/nvme1n1 /var/lib/docker ext4 rw,relatime 0 0
/dev/sdb1 /mnt/backup\040drive ext4 ro,relatime 0 0
cgroup2 /sys/fs/cgroup cgroup2 rw,nosuid,nodev,noexec,relatime 0 0
`

func TestParseProcMounts(t *testing.T) {
	got := parseProcMounts(procMounts)
	want := []string{"/", "/boot/efi", "/data"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Mount != w {
			t.Fatalf("entry %d = %q, want %q", i, got[i].Mount, w)
		}
	}
	// /var/lib/docker is the same device as /data → deduped, so the tightest-volume pick cannot
	// depend on which name happened to sort first. The read-only backup mount (escaped path) and
	// every squashfs/pseudo mount are gone.
	if len(parseProcMounts("")) != 0 || len(parseProcMounts("garbage\nnot a mount line\n")) != 0 {
		t.Fatal("accepted junk")
	}
	// Escaping is still handled where the mount IS eligible.
	esc := parseProcMounts(`/dev/sdc1 /mnt/my\040disk ext4 rw,relatime 0 0` + "\n")
	if len(esc) != 1 || esc[0].Mount != "/mnt/my disk" {
		t.Fatalf("escaping: %+v", esc)
	}
}

func TestDiskStatFrom(t *testing.T) {
	const bs = 4096
	const blocks = 10 << 18 // 10 GiB worth of 4 KiB blocks
	// 30% used by the kernel's count, with 5% reserved for root: used-percent is measured against
	// what a build could actually use, so it reads slightly higher than blocks-minus-free.
	d, ok := diskStatFrom("/data", blocks, blocks*70/100, blocks*65/100, bs)
	if !ok {
		t.Fatal("not parsed")
	}
	if d.Mount != "/data" {
		t.Fatalf("mount %q", d.Mount)
	}
	if math.Abs(d.UsedPct-31.578) > 0.01 { // 30 / (30+65)
		t.Fatalf("used %v", d.UsedPct)
	}
	if math.Abs(d.FreeGB-6.5) > 0.001 {
		t.Fatalf("free %v", d.FreeGB)
	}
	// A full volume is 100%, not a missing reading.
	if d, ok := diskStatFrom("/full", blocks, 0, 0, bs); !ok || d.UsedPct != 100 || d.FreeGB != 0 {
		t.Fatalf("full: %+v %v", d, ok)
	}
	// Nonsense from statfs, and the sub-1GiB volumes macOS is littered with, produce nothing.
	for _, c := range [][4]uint64{
		{blocks, 0, 0, 0},               // zero block size
		{0, 0, 0, bs},                   // zero blocks
		{blocks, blocks * 2, 0, bs},     // more free than total
		{blocks, 0, blocks * 2, bs},     // more available than total
		{1 << 16, 1 << 15, 1 << 15, bs}, // 256 MiB system volume → below the floor
	} {
		if _, ok := diskStatFrom("/x", c[0], c[1], c[2], c[3]); ok {
			t.Fatalf("accepted %v", c)
		}
	}
}

func TestTightestDisk(t *testing.T) {
	// The TIGHTEST volume wins, not the root one: a box whose /data is full cannot build.
	got, ok := tightestDisk([]diskStat{
		{Mount: "/", UsedPct: 12, FreeGB: 400},
		{Mount: "/data", UsedPct: 97, FreeGB: 3},
		{Mount: "/boot/efi", UsedPct: 40, FreeGB: 0.3},
	})
	if !ok || got.Mount != "/data" || got.UsedPct != 97 {
		t.Fatalf("got %+v %v", got, ok)
	}
	// Ties break on mount path, so the reported volume does not flicker between beats.
	got, _ = tightestDisk([]diskStat{{Mount: "/b", UsedPct: 50}, {Mount: "/a", UsedPct: 50}})
	if got.Mount != "/a" {
		t.Fatalf("tie: %+v", got)
	}
	if _, ok := tightestDisk(nil); ok {
		t.Fatal("accepted an empty candidate list")
	}
}

// TestNodeMetricsAbsentNotZero is the contract test: an unmeasured field must be an ABSENT KEY, and
// a measured ZERO must survive the wire. The server drops absent keys and clamps the rest, so this
// is the difference between "never reported memory" and "has no memory" on the fleet card.
func TestNodeMetricsAbsentNotZero(t *testing.T) {
	zero, mount := 0.0, "/"
	up := int64(0)
	b, err := json.Marshal(api.DaemonConfig{Metrics: &api.NodeMetrics{
		CPUPct: &zero, Load1: &zero, DiskUsedPct: &zero, DiskFreeGB: &zero, DiskMount: mount, UptimeS: &up,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Metrics map[string]any `json:"metrics"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	// Measured zeroes are PRESENT (this is why they are pointers).
	for _, k := range []string{"cpu_pct", "load1", "disk_used_pct", "disk_free_gb", "uptime_s"} {
		if _, ok := out.Metrics[k]; !ok {
			t.Fatalf("%s was dropped; a measured 0 must reach the server", k)
		}
	}
	// Unmeasured fields are ABSENT, never 0.
	for _, k := range []string{"cpu_cores", "mem_used_pct", "mem_total_mb"} {
		if _, ok := out.Metrics[k]; ok {
			t.Fatalf("%s is present but was never measured", k)
		}
	}
	// And nothing at all measured means no `metrics` key on the config at all.
	b, err = json.Marshal(api.DaemonConfig{Metrics: nil})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["metrics"]; ok {
		t.Fatal("empty metrics block was sent")
	}
	if !(&api.NodeMetrics{}).Empty() || (&api.NodeMetrics{CPUCores: 8}).Empty() {
		t.Fatal("Empty() is wrong")
	}
}

// TestCollectNodeMetricsIsSane runs the real collector on whatever host the tests are on. It does
// NOT assert the machine has RAM — it asserts the INVARIANTS: every value present is inside the
// range the server declares, and collection never panics or hangs.
func TestCollectNodeMetricsIsSane(t *testing.T) {
	done := make(chan *api.NodeMetrics, 1)
	go func() { done <- collectNodeMetrics() }()
	var m *api.NodeMetrics
	select {
	case m = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("collection did not finish; it must never be able to wedge a heartbeat")
	}
	if m == nil {
		return // a platform we omit everything on is allowed; it just sends no metrics block
	}
	inRange := func(name string, v *float64, min, max float64) {
		if v != nil && (*v < min || *v > max || math.IsNaN(*v) || math.IsInf(*v, 0)) {
			t.Fatalf("%s = %v, outside %v..%v", name, *v, min, max)
		}
	}
	inRange("cpu_pct", m.CPUPct, 0, 100)
	inRange("load1", m.Load1, 0, 1024)
	inRange("mem_used_pct", m.MemUsedPct, 0, 100)
	inRange("disk_used_pct", m.DiskUsedPct, 0, 100)
	inRange("disk_free_gb", m.DiskFreeGB, 0, 1_000_000)
	if m.CPUCores < 1 || m.CPUCores > 4096 {
		t.Fatalf("cpu_cores = %d", m.CPUCores) // always available: runtime.NumCPU()
	}
	if m.MemTotalMB < 0 || m.MemTotalMB > 16_777_216 {
		t.Fatalf("mem_total_mb = %d", m.MemTotalMB)
	}
	if len(m.DiskMount) > 64 {
		t.Fatalf("disk_mount is longer than the server keeps: %q", m.DiskMount)
	}
	// load1 without cpu_cores is an unreadable number on the card; the collector must never do it.
	if m.Load1 != nil && m.CPUCores == 0 {
		t.Fatal("load1 reported without cpu_cores")
	}
	// The disk numbers must always say WHICH volume they describe.
	if (m.DiskUsedPct != nil || m.DiskFreeGB != nil) && m.DiskMount == "" {
		t.Fatal("disk numbers reported with no disk_mount")
	}
	if m.UptimeS != nil && (*m.UptimeS < 0 || *m.UptimeS > 3_155_760_000) {
		t.Fatalf("uptime_s = %d", *m.UptimeS)
	}
}

// TestSystemOnlyMount pins the macOS rule: every volume of an APFS container reports the same
// container-level numbers, so only the one a human recognises (Data) may carry them.
func TestSystemOnlyMount(t *testing.T) {
	for _, m := range []string{"/System/Volumes/VM", "/System/Volumes/Preboot", "/System/Volumes/Update", "/System/Volumes/xarts"} {
		if !systemOnlyMount(m) {
			t.Fatalf("%s should be skipped", m)
		}
	}
	for _, m := range []string{"/", "/System/Volumes/Data", "/Volumes/Backup", "/data", "/var/lib/docker"} {
		if systemOnlyMount(m) {
			t.Fatalf("%s should be kept", m)
		}
	}
	// And it is enforced where the numbers are built, not just advisory.
	if _, ok := diskStatFrom("/System/Volumes/Update", 1<<30, 1<<29, 1<<29, 4096); ok {
		t.Fatal("a container sibling reached the candidate list")
	}
}
