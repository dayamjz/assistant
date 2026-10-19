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

// GitLabHostname is the code host the GitLab adapter addresses. It is the
// only host GitLabRepository will read a specifier out of, and the only
// network host a GitLab request is sent to. A self-hosted GitLab is a
// different host and this adapter does not address one, for the same reason
// GitHubHostname does not address GitHub Enterprise.
const GitLabHostname = "gitlab.com"

// gitLabAPI is the base URL every GitLab request is built from when no
// loopback stand-in is named. It is not read from the environment.
const gitLabAPI = "https://gitlab.com/api/v4"

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

// GitLabRepository returns the specifier the GitLab adapter addresses the
// repository at remote by, and reports whether remote named one.
//
// It reads only a remote on GitLabHostname, on GitHubRepository's terms: a
// remote on any other host, including a self-hosted GitLab, is not a
// repository this adapter can address. Returning a path out of one anyway
// would send a merge request to the gitlab.com project that happens to share
// the name, because the path this package puts on a request names no host.
//
// A GitLab project path may name a group under a group. Each segment is held
// to the same characters validRepository allows, and a segment of "." or ".."
// is refused. A remote it does not recognize is a false second result rather
// than an error, on GitHubRepository's terms.
func GitLabRepository(remote string) (string, bool) {
	host, path, ok := splitRemote(strings.TrimSpace(remote))
	if !ok || !strings.EqualFold(host, GitLabHostname) {
		return "", false
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if !validGitLabRepository(path) {
		return "", false
	}
	return path, true
}

// HostedRepository returns the specifier and the host kind remote names when
// this build addresses that host. A remote it does not address returns a
// false ok and the kind ClassifyRemote would report, so a caller can tell a
// local path from a host this build does not talk to without a second parse.
//
// GitHub is tried first and GitLab second. The two hostnames do not overlap,
// so a remote is never a specifier on both.
func HostedRepository(remote string) (spec string, kind RemoteKind, ok bool) {
	if spec, ok = GitHubRepository(remote); ok {
		return spec, RemoteGitHub, true
	}
	if spec, ok = GitLabRepository(remote); ok {
		return spec, RemoteGitLab, true
	}
	return "", ClassifyRemote(remote), false
}

// splitRemote separates a git remote URL into the host it names and the path
// under it. It reports false for text that is neither of the two shapes a
// remote takes.
//
// The host is returned without its userinfo and without its port, so a remote
// carrying either is recognized as the host it names rather than refused. The
// ssh form of a GitHub remote carries both.
//
// Dropping the port has a consequence worth naming: a remote on this host at a
// non-standard port yields the same specifier as one at the standard port. It
// decides nothing about where a write lands, because a specifier carries no
// host and every invocation is given GH_HOST, so what a port here could change
// is which remotes are recognized and not which host is addressed.
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

// validScheme reports whether s is written in the characters a URL scheme
// takes. It is what keeps text that merely contains "://" - a sentence, a
// message quoting a URL - from being read as one, since a remote arrives here
// as a stored field and this package does not control what was stored there.
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

// validGitLabRepository reports whether spec is a GitLab project path: two or
// more segments, each a repository segment, none of them "." or "..". One
// segment is a GitLab user or group snippet, not a project, and is refused.
func validGitLabRepository(spec string) bool {
	parts := strings.Split(spec, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || !validRepositorySegment(part) {
			return false
		}
	}
	return true
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
