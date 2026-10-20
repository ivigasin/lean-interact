package leaninteract

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// processTreeRSS returns the resident memory of pid and all its descendants
// in bytes, read from /proc.
func processTreeRSS(pid int) uint64 {
	children := map[int][]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// Fields after the parenthesised command name: state ppid ...
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		children[ppid] = append(children[ppid], p)
	}
	page := uint64(os.Getpagesize())
	var total uint64
	for _, p := range descendants(pid, children) {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p), "statm"))
		if err != nil {
			continue
		}
		f := strings.Fields(string(b))
		if len(f) >= 2 {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			total += n * page
		}
	}
	return total
}

// systemMemoryPercent returns the used share of system memory in [0, 100].
func systemMemoryPercent() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	var total, avail float64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		n, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 64)
		switch k {
		case "MemTotal":
			total = n
		case "MemAvailable":
			avail = n
		}
	}
	if total == 0 {
		return 0
	}
	return 100 * (total - avail) / total
}
