package node

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// rss reads /proc/self/statm: the second field is resident pages.
func rss() uint64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseUint(f[1], 10, 64)
	return pages * uint64(os.Getpagesize())
}

// footprint is the anonymous part of RSS (RssAnon in /proc/self/status):
// what the process itself dirtied, without the binary's text pages. Linux
// keeps no high-water mark for it, so peak is 0.
func footprint() (now, peak uint64) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "RssAnon:"); ok {
			kb, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb << 10, 0
		}
	}
	return 0, 0
}

// maxRSS is the process's resident high-water mark. Linux reports KiB.
func maxRSS() uint64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return uint64(ru.Maxrss) << 10
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
