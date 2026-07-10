package update

import "strings"

func normalizeVersion(v string) string {
	trimmed := strings.TrimSpace(v)
	trimmed = strings.TrimPrefix(trimmed, "v")
	if idx := strings.IndexAny(trimmed, "+-"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return trimmed
}

// DisplayVersion renders a tag-like version for user-facing output.
func DisplayVersion(v string) string {
	normalized := normalizeVersion(v)
	if normalized == "" {
		return ""
	}
	return "v" + normalized
}

func compareVersions(a, b string) int {
	left := parseVersionParts(normalizeVersion(a))
	right := parseVersionParts(normalizeVersion(b))
	maxLen := len(left)
	if len(right) > maxLen {
		maxLen = len(right)
	}
	for i := 0; i < maxLen; i++ {
		var l, r int
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	return 0
}

func parseVersionParts(v string) []int {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n := 0
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				break
			}
			n = n*10 + int(part[i]-'0')
		}
		out = append(out, n)
	}
	return out
}
