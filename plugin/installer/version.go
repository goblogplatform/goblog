package installer

import (
	"strconv"
	"strings"
)

// parseVersion reads "1.2.3" or "v1.2.3" into three ints.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func cmp(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Compatible reports whether a goblog at version running satisfies a
// plugin's (or theme's) min_goblog_version. Dev builds ("development",
// "latest", or anything unparseable) and an unparseable minimum are
// treated as compatible.
func Compatible(running, min string) bool {
	r, ok := parseVersion(running)
	if !ok {
		return true
	}
	m, ok := parseVersion(min)
	if !ok {
		return true
	}
	return cmp(r, m) >= 0
}

// Newer reports whether candidate is a strictly newer version than current.
func Newer(candidate, current string) bool {
	c, ok1 := parseVersion(candidate)
	u, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	return cmp(c, u) > 0
}
