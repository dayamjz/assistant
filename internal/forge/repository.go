package forge

import (
	"strings"
)

// GitHubHostname is the code host the GitHub adapter addresses. It is one
// constant because two facts depend on it and they must not disagree: it is
// the host every invocation is given in GH_HOST, and it is the only host
// GitHubRepository will read a specifier out of.
//
// PRD section 8 asks for one adapter per provider, and this is that provider.
// A GitHub Enterprise installation is a different host and this adapter does
// not address one; what that costs is stated in GitHubRepository.
const GitHubHostname = "github.com"

// GitHubRepository returns the specifier the GitHub adapter addresses the
// repository at remote by, and reports whether remote named one.
//
// remote is a git remote URL as internal/store holds it, which is to say after
// internal/redact has run over it: a credential in the userinfo has already
// been replaced, and this reads the host and the path, so a redacted URL and
// the credentialed one it came from produce the same specifier.
//
// It reads only a remote on GitHubHostname. A remote on any other host is not
// a repository this adapter can address, and returning owner/name out of one
// anyway is how a run opens a pull request against the github.com repository
// that happens to share the name: the specifier this package puts on a command
// line carries no host, so the host has to be established here or not at all.
//
// A remote it does not recognize is a false second result rather than an
// error, because "this repository is not on a host this build talks to" is an
// answer about the repository and not a failure of this call. A caller with no
// specifier has no provider, and the stage that needed one refuses.
//
// The two shapes it reads are the two a git remote takes: a URL with a scheme,
// including the ssh:// form, and scp-like git@host:owner/name. A trailing
// ".git" and a trailing slash are removed, and nothing else in the path is
// accepted, so a URL naming something deeper than a repository is not read as
// the repository above it.
func GitHubRepository(remote string) (string, bool) {
	host, path, ok := splitRemote(strings.TrimSpace(remote))
	if !ok || !strings.EqualFold(host, GitHubHostname) {
		return "", false
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if !validRepository(path) {
		return "", false
	}
	return path, true
}

// splitRemote separates a git remote URL into the host it names and the path
// under it. It reports false for text that is neither of the two shapes a
// remote takes.
//
// The host is returned without its userinfo and without its port, so a remote
// carrying either is recognized as the host it names rather than refused. A
// port is dropped rather than checked because what the specifier decides is
// which repository is addressed, and GH_HOST is what decides where the
// provider looks for it.
func splitRemote(remote string) (host, path string, ok bool) {
	if scheme, rest, found := strings.Cut(remote, "://"); found {
		if !validScheme(scheme) {
			return "", "", false
		}
		authority, p, _ := strings.Cut(rest, "/")
		return hostOf(authority), p, authority != ""
	}
	// The scp-like form: an authority, a colon, then the path. It is only
	// recognized when the colon comes before any slash, so a plain path
	// holding a colon later on is not read as a remote.
	authority, p, found := strings.Cut(remote, ":")
	if !found || strings.Contains(authority, "/") || authority == "" {
		return "", "", false
	}
	return hostOf(authority), p, true
}

// hostOf returns the host named by an authority, dropping any userinfo before
// it and any port after it.
func hostOf(authority string) string {
	if _, after, found := strings.Cut(authority, "@"); found {
		authority = after
	}
	if before, _, found := strings.Cut(authority, ":"); found {
		authority = before
	}
	return authority
}

// validScheme reports whether s is written as a URL scheme. It is here so that
// a Windows path such as C:\repo, whose drive letter and colon read as an
// scp-like authority, is not taken for a remote.
func validScheme(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '+' || r == '.' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// validRepository reports whether spec is owner/name written in the characters
// GitHub allows in each, and nothing else. A scheme, userinfo, a host, or a
// third path segment all fail it, and so does a segment that would be read as
// an option.
func validRepository(spec string) bool {
	owner, name, ok := strings.Cut(spec, "/")
	if !ok {
		return false
	}
	return validRepositorySegment(owner) && validRepositorySegment(name)
}

func validRepositorySegment(seg string) bool {
	if seg == "" || len(seg) > 100 || strings.HasPrefix(seg, "-") {
		return false
	}
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
