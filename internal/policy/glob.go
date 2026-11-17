package policy

import "strings"

// globMatch matches a pattern against a value with two wildcards:
//
//   - matches within one path segment (no "/")
//     **  matches across segments
//
// Written by hand rather than translated to a regexp so the semantics are
// exactly these two, with no accidental regexp metacharacters: a route like
// /v1/payments/{id} or a service like payment* must never be treated as a
// pattern in a way a reader cannot predict.
func globMatch(pattern, value string) bool {
	if pattern == "" {
		return value == ""
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(value, "/"))
}

func matchSegments(pat, val []string) bool {
	for len(pat) > 0 {
		p := pat[0]
		switch {
		case p == "**":
			// Absorb zero or more segments.
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(val); i++ {
				if matchSegments(pat[1:], val[i:]) {
					return true
				}
			}
			return false
		case len(val) == 0:
			return false
		case p == "*":
			// Single segment wildcard.
		default:
			if !matchSegment(p, val[0]) {
				return false
			}
		}
		pat = pat[1:]
		val = val[1:]
	}
	return len(val) == 0
}

// matchSegment matches one segment, where "*" matches any run of characters.
func matchSegment(pattern, value string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			if i == len(parts)-1 {
				return true
			}
			continue
		}
		idx := strings.Index(value, part)
		if idx < 0 {
			return false
		}
		value = value[idx+len(part):]
	}
	return len(parts) == 0 || strings.HasSuffix(pattern, "*") || value == ""
}
