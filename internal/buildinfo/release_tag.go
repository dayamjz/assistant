package buildinfo

import "strings"

// WellFormedReleaseTag reports whether version is a v-prefixed SemVer 2.0.0
// tag the release workflow may build and publish: MAJOR.MINOR.PATCH without
// leading zeros in numeric fields, and an optional prerelease of dot-separated
// identifiers that are never empty and never carry a leading zero when numeric.
func WellFormedReleaseTag(version string) bool {
	if !strings.HasPrefix(version, "v") {
		return false
	}
	rest := version[1:]
	if rest == "" {
		return false
	}
	core, pre, hasPre := rest, "", false
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		core, pre = rest[:i], rest[i+1:]
		hasPre = true
	}
	if !wellFormedReleaseCore(core) {
		return false
	}
	if !hasPre {
		return true
	}
	return wellFormedPrerelease(pre)
}

func wellFormedReleaseCore(core string) bool {
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if !wellFormedNumericIdentifier(part) {
			return false
		}
	}
	return true
}

func wellFormedPrerelease(pre string) bool {
	if pre == "" {
		return false
	}
	for _, id := range strings.Split(pre, ".") {
		if !wellFormedPrereleaseIdentifier(id) {
			return false
		}
	}
	return true
}

func wellFormedPrereleaseIdentifier(id string) bool {
	if id == "" {
		return false
	}
	if isAllDigits(id) {
		return wellFormedNumericIdentifier(id)
	}
	for _, c := range id {
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func wellFormedNumericIdentifier(id string) bool {
	if id == "0" {
		return true
	}
	if id == "" || id[0] == '0' {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
