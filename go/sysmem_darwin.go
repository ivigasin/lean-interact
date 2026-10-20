package leaninteract

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var vmStatLine = regexp.MustCompile(`^(.+?):\s+(\d+)\.?$`)

// systemMemoryPercent mirrors psutil on macOS: available = free + inactive.
func systemMemoryPercent() float64 {
	memsize, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	total, _ := strconv.ParseFloat(strings.TrimSpace(string(memsize)), 64)
	out, err := exec.Command("vm_stat").Output()
	if err != nil || total == 0 {
		return 0
	}
	pageSize := 4096.0
	pages := map[string]float64{}
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 {
			if j := strings.Index(line, "page size of "); j >= 0 {
				if n, err := strconv.ParseFloat(strings.Fields(line[j+len("page size of "):])[0], 64); err == nil {
					pageSize = n
				}
			}
			continue
		}
		if m := vmStatLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			pages[m[1]], _ = strconv.ParseFloat(m[2], 64)
		}
	}
	avail := (pages["Pages free"] + pages["Pages inactive"]) * pageSize
	return 100 * (total - avail) / total
}
