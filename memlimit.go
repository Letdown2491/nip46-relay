package main

import (
	"log"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// configureMemory tunes the Go runtime for the detected memory ceiling and
// returns the byte-budget the in-memory event store may use.
//
// Two coordinated bounds keep the relay alive on small boxes:
//   - GOMEMLIMIT (auto-set here only when the operator hasn't configured one)
//     makes the GC defend a process-wide ceiling instead of letting the heap
//     overshoot to ~2x the live set (GOGC=100) and OOM.
//   - the returned event budget caps what the store actually retains.
func configureMemory() int64 {
	total := detectMemoryLimit()

	// debug.SetMemoryLimit(-1) reports the limit currently in effect: one the
	// operator set via the GOMEMLIMIT env var, or the runtime default of
	// math.MaxInt64 ("off"). Respect an explicit limit; only auto-tune when unset.
	limit := debug.SetMemoryLimit(-1)
	operatorSet := limit != math.MaxInt64

	switch {
	case operatorSet:
		log.Printf("Memory: respecting configured GOMEMLIMIT (%d MB)", limit>>20)
	case total > 0:
		// Reserve headroom for the OS and non-heap runtime memory (stacks,
		// mmap'd files, etc.): 20% of total, but at least 256 MB.
		reserve := total / 5
		if reserve < 256<<20 {
			reserve = 256 << 20
		}
		if l := total - reserve; l > 0 {
			debug.SetMemoryLimit(l)
			log.Printf("Memory: detected %d MB, GOMEMLIMIT set to %d MB", total>>20, l>>20)
		}
	default:
		log.Printf("Memory: could not detect a limit; leaving GOMEMLIMIT at default")
	}

	// Event byte-budget for the in-memory store.
	switch {
	case config.MaxMemoryMB > 0:
		return int64(config.MaxMemoryMB) << 20
	case operatorSet:
		// Half the configured soft limit; the rest is runtime + GC headroom.
		return limit / 2
	case total > 0:
		// ~50% of total for event payloads; the rest covers indexes, the Go
		// runtime, and GC headroom under the auto-set GOMEMLIMIT.
		return total / 2
	default:
		return 256 << 20 // conservative fallback when the limit is unknown
	}
}

// detectMemoryLimit returns the memory ceiling in bytes available to this
// process, preferring cgroup limits (containers) over host RAM. Returns 0 when
// no limit can be determined.
func detectMemoryLimit() int64 {
	if v := readCgroupV2(); v > 0 {
		return v
	}
	if v := readCgroupV1(); v > 0 {
		return v
	}
	return readMemTotal()
}

func readCgroupV2() int64 {
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0 // unlimited
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

func readCgroupV1() int64 {
	b, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes")
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	if v > (1 << 60) {
		return 0 // cgroup v1 sentinel for "unlimited"
	}
	return v
}

func readMemTotal() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return kb * 1024 // /proc/meminfo reports kB
			}
		}
	}
	return 0
}
