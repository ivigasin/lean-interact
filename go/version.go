package leaninteract

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ParseLeanVersion normalizes a Lean version such as "leanprover/lean4:v4.19.0"
// to "v4.19.0". Only Lean 4 versions are accepted.
func ParseLeanVersion(v string) (string, error) {
	v = strings.TrimPrefix(v, "leanprover/lean4:")
	if !strings.HasPrefix(v, "v4") {
		return "", errors.New("leaninteract: unable to parse Lean version format")
	}
	return v, nil
}

// ProjectLeanVersion returns the Lean version declared in dir/lean-toolchain,
// or "" if the file is missing or unparsable.
func ProjectLeanVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "lean-toolchain"))
	if err != nil {
		return ""
	}
	v, err := ParseLeanVersion(strings.TrimSpace(string(b)))
	if err != nil {
		return ""
	}
	return v
}

type parsedVersion struct {
	release []int
	pre     string // "" for a final release
	preNum  int
	rest    string
}

func parseVersion(v string) parsedVersion {
	v = strings.TrimPrefix(v, "v")
	var p parsedVersion
	relPart, suffix, _ := strings.Cut(v, "-")
	for _, f := range strings.Split(relPart, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			p.rest = v
			break
		}
		p.release = append(p.release, n)
	}
	if suffix != "" {
		i := 0
		for i < len(suffix) && (suffix[i] < '0' || suffix[i] > '9') {
			i++
		}
		p.pre = strings.ToLower(strings.TrimRight(suffix[:i], ".-_"))
		p.preNum, _ = strconv.Atoi(suffix[i:])
	}
	return p
}

func preRank(pre string) int {
	switch pre {
	case "":
		return 4
	case "a", "alpha":
		return 1
	case "b", "beta":
		return 2
	case "c", "rc", "pre", "preview":
		return 3
	}
	return 0
}

// CompareLeanVersions orders versions such as "v4.9.0-rc1" < "v4.9.0" <
// "v4.10.0" (numeric components; a pre-release sorts before its release).
func CompareLeanVersions(a, b string) int {
	pa, pb := parseVersion(a), parseVersion(b)
	for i := 0; i < max(len(pa.release), len(pb.release)); i++ {
		var x, y int
		if i < len(pa.release) {
			x = pa.release[i]
		}
		if i < len(pb.release) {
			y = pb.release[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	if ra, rb := preRank(pa.pre), preRank(pb.pre); ra != rb {
		if ra < rb {
			return -1
		}
		return 1
	}
	if pa.pre != pb.pre {
		return strings.Compare(pa.pre, pb.pre)
	}
	if pa.preNum != pb.preNum {
		if pa.preNum < pb.preNum {
			return -1
		}
		return 1
	}
	return strings.Compare(pa.rest, pb.rest)
}

// SortLeanVersions sorts versions in place in ascending order.
func SortLeanVersions(vs []string) {
	sort.SliceStable(vs, func(i, j int) bool { return CompareLeanVersions(vs[i], vs[j]) < 0 })
}
