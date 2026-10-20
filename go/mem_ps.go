//go:build unix && !linux

package leaninteract

import (
	"os/exec"
	"strconv"
	"strings"
)

// processTreeRSS returns the resident memory of pid and all its descendants
// in bytes, using ps(1).
func processTreeRSS(pid int) uint64 {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,rss=").Output()
	if err != nil {
		return 0
	}
	children := map[int][]int{}
	rss := map[int]uint64{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		p, _ := strconv.Atoi(f[0])
		pp, _ := strconv.Atoi(f[1])
		kb, _ := strconv.ParseUint(f[2], 10, 64)
		children[pp] = append(children[pp], p)
		rss[p] = kb * 1024
	}
	var total uint64
	for _, p := range descendants(pid, children) {
		total += rss[p]
	}
	return total
}
