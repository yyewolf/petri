package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// version is a parsed module version such as v1.37.1 or v1.38.0-rc.1.
type version struct {
	Major, Minor, Patch int
	Pre                 string
}

// parseVersion parses vMAJOR.MINOR.PATCH[-PRERELEASE].
func parseVersion(s string) (version, bool) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}

	var nums [3]int

	for i := range nums {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return version{}, false
		}

		nums[i] = n
	}

	return version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: m[4]}, true
}

func (v version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}

	return s
}

func (v version) stable() bool {
	return v.Pre == ""
}

// goVersionLess reports whether Go version a (such as "1.26" or "1.26.5") is
// older than b. Missing components count as zero.
func goVersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")

	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}

		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}

		if x != y {
			return x < y
		}
	}

	return false
}
