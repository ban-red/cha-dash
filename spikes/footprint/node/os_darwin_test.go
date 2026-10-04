package node

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// rss asks ps: macOS has no /proc, and task_info needs cgo.
func rss() uint64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	return kb << 10
}

// footprint is macOS's "physical footprint" (what Activity Monitor calls
// Memory): dirty and compressed pages charged to the process, without clean
// shared pages such as the binary's text and system libraries. It's the
// nearest macOS number to Linux's anonymous RSS. Read with vmmap, so it's
// only taken at stage samples.
func footprint() (now, peak uint64) {
	out, err := exec.Command("vmmap", "-summary", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0, 0
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "Physical footprint (peak):"); ok {
			peak = parseSize(v)
		} else if v, ok := strings.CutPrefix(line, "Physical footprint:"); ok {
			now = parseSize(v)
		}
	}
	return now, peak
}

// parseSize reads vmmap sizes such as "864K", "52.3M" or "1.2G".
func parseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'K':
		mult = 1 << 10
	case 'M':
		mult = 1 << 20
	case 'G':
		mult = 1 << 30
	}
	f, _ := strconv.ParseFloat(strings.TrimRight(s, "KMGB"), 64)
	return uint64(f * mult)
}

// maxRSS is the process's resident high-water mark. macOS reports bytes.
func maxRSS() uint64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return uint64(ru.Maxrss)
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
