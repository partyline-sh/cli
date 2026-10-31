package main

import (
	"os"
	"syscall"
)

// nodemetrics_linux.go — the Linux I/O behind collectNodeMetrics. Everything here is a read of a
// /proc file plus statfs(2): no subprocess, no dependency, nothing that can block on the network.
// The parsing all lives in nodemetrics.go so it is testable from any host.

// readLoad1 returns the 1-minute load average. Meaningless without cpu_cores, which the collector
// always sends alongside it.
func readLoad1() (float64, bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	return parseProcLoadavg(string(b))
}

// readCPUPct returns utilisation SINCE THE PREVIOUS BEAT, as the delta of /proc/stat's counters.
// The first call after start stores a sample and reports nothing: one sample cannot be a
// percentage, and inventing one (or sleeping 200ms to get a second) is worse than an empty field
// for one minute.
func readCPUPct() (float64, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	cur, ok := parseProcStat(string(b))
	if !ok {
		return 0, false
	}
	cpuPrev.Lock()
	defer cpuPrev.Unlock()
	prev, had := cpuPrev.sample, cpuPrev.have
	cpuPrev.sample, cpuPrev.have = cur, true
	if !had {
		return 0, false
	}
	return cpuPctFromSamples(prev, cur)
}

// readMemory returns total MB + used percent from /proc/meminfo.
func readMemory() (int64, float64, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	return parseMeminfo(string(b))
}

// readUptime returns seconds since boot from /proc/uptime.
func readUptime() (int64, bool) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	return parseProcUptime(string(b))
}

// diskCandidates statfs's every real read-write volume in /proc/mounts. A mount that has gone away
// between the read and the statfs is skipped, not fatal.
func diskCandidates() []diskStat {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	var out []diskStat
	for _, m := range parseProcMounts(string(b)) {
		var st syscall.Statfs_t
		if syscall.Statfs(m.Mount, &st) != nil {
			continue
		}
		d, ok := diskStatFrom(m.Mount, st.Blocks, st.Bfree, st.Bavail, uint64(st.Bsize))
		if ok {
			out = append(out, d)
		}
	}
	return out
}
