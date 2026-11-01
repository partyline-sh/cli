package main

import (
	"strconv"
	"strings"
	"syscall"
	"time"
)

// nodemetrics_darwin.go — the macOS I/O behind collectNodeMetrics. There is no /proc here, so the
// sources are sysctl(8)/vm_stat(1)/top(1) — all of which ship with macOS — plus getfsstat(2) for
// the mount table. Every subprocess goes through runBounded, so the worst a hung tool can do is
// cost its own metric. The parsing all lives in nodemetrics.go so it is testable from any host.

// readLoad1 returns the 1-minute load average.
func readLoad1() (float64, bool) {
	out, ok := runBounded("sysctl", "-n", "vm.loadavg")
	if !ok {
		return 0, false
	}
	return parseSysctlLoadavg(out)
}

// readCPUPct returns utilisation over a short interval, from top's SECOND sample.
//
// macOS has no cumulative CPU counter reachable without cgo (kern.cp_time is FreeBSD's, not
// Darwin's; host_processor_info is a Mach call), so unlike Linux this cannot be a delta between
// beats and has to sample. `top -l 2 -n 0 -s 0` is the cheap form of that: -n 0 suppresses the
// process list entirely and -s 0 makes the gap between the two samples as short as possible, which
// measured ~0.6s wall. Bounded by runBounded like everything else.
func readCPUPct() (float64, bool) {
	out, ok := runBounded("top", "-l", "2", "-n", "0", "-s", "0")
	if !ok {
		return 0, false
	}
	return parseTopCPU(out)
}

// readMemory returns total MB + used percent. Total comes from hw.memsize (the machine's real RAM);
// used comes from vm_stat's page counts, which are the only breakdown available without cgo. The
// two are deliberately from different sources — the page counts need not sum to hw.memsize, so the
// percentage is computed against the authoritative total and clamped.
func readMemory() (int64, float64, bool) {
	out, ok := runBounded("sysctl", "-n", "hw.memsize")
	if !ok {
		return 0, 0, false
	}
	total, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil || total == 0 {
		return 0, 0, false
	}
	totalMB := int64(total / (1 << 20))

	vm, ok := runBounded("vm_stat")
	if !ok {
		return totalMB, 0, false // total alone is not a reading; the collector needs both or neither
	}
	used, ok := parseVMStat(vm)
	if !ok {
		return totalMB, 0, false
	}
	return totalMB, clampPct(float64(used) / float64(total) * 100), true
}

// readUptime derives seconds since boot from kern.boottime.
func readUptime() (int64, bool) {
	out, ok := runBounded("sysctl", "-n", "kern.boottime")
	if !ok {
		return 0, false
	}
	return parseBoottime(out, time.Now())
}

// diskCandidates enumerates mounted volumes with getfsstat(2) — one syscall, no subprocess, and it
// hands back the statfs numbers directly so nothing has to be re-stat'ed.
//
// READ-ONLY VOLUMES ARE SKIPPED, and on macOS that is not a detail: the sealed system volume `/`
// and every mounted disk image (Xcode simulator runtimes sit at 98% full by construction) would
// otherwise win the tightest-volume contest on every laptop in the fleet and report a permanent
// false "disk nearly full". What is left on a normal Mac is /System/Volumes/Data — the volume that
// actually holds the user's checkouts, which is the honest answer to "can this machine build".
func diskCandidates() []diskStat {
	n, err := syscall.Getfsstat(nil, MNT_NOWAIT)
	if err != nil || n <= 0 {
		return nil
	}
	buf := make([]syscall.Statfs_t, n+8) // headroom: a volume can appear between the count and the read
	n, err = syscall.Getfsstat(buf, MNT_NOWAIT)
	if err != nil || n <= 0 {
		return nil
	}
	var out []diskStat
	for _, st := range buf[:n] {
		if st.Flags&MNT_RDONLY != 0 {
			continue
		}
		if !realFSTypes[cstring(st.Fstypename[:])] {
			continue
		}
		d, ok := diskStatFrom(cstring(st.Mntonname[:]), st.Blocks, st.Bfree, st.Bavail, uint64(st.Bsize))
		if ok {
			out = append(out, d)
		}
	}
	return out
}

// getfsstat/statfs flags. Spelled out here rather than taken from syscall because the darwin
// package does not export them under stable names across Go versions.
const (
	MNT_RDONLY = 0x00000001
	MNT_NOWAIT = 2 // do not block on a server; a stale network mount must not wedge a heartbeat
)

// cstring turns a fixed-size NUL-padded C string field into a Go string.
func cstring(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}
