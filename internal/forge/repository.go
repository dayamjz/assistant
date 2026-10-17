package forge

import (
	"strconv"
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

// Reach is what this build can do on a code host for a repository whose
// upstream is a given git remote. The set is closed, and the zero value is the
// strict answer: a remote nothing below recognizes is one a run is refused
// over rather than one it walks without a code host.
type Reach uint8

const (
	// ReachUnaddressable is a remote that names a host this build does not
	// address, or no remote at all. A run of such a repository is refused
	// before it starts: the remote may well be a code host, and treating it as
	// none would skip the stages that need one in a repository that has one.
	ReachUnaddressable Reach = iota
	// ReachHosted is a remote on GitHubHostname that GitHubRepository reads a
	// specifier out of, which is the one repository this build opens a pull
	// request on.
	ReachHosted
	// ReachLocal is a remote that names no network host at all: a file:// URL
	// or a path. Issue #283's owner decision is that a run of such a
	// repository pushes as usual and passes over the pull request and checks
	// stages, because there is no code host to open a pull request on.
	ReachLocal
)

// String renders the reach for a diagnostic.
func (r Reach) String() string {
	switch r {
	case ReachHosted:
		return "hosted"
	case ReachLocal:
		return "local"
	case ReachUnaddressable:
		return "unaddressable"
	}
	return "reach(" + strconv.Itoa(int(r)) + ")"
}

// ReachOf answers what this build can do on a code host for the repository at
// remote, and the specifier GitHubRepository reads out of it when the answer
// is ReachHosted, empty otherwise.
//
// It tells the three apart from the remote's text alone and asks nothing over
// a network, so what it establishes is the shape of the remote, not that a
// repository answers there:
//
//   - A remote GitHubRepository reads is ReachHosted.
//   - A remote whose scheme is file, and a remote with no scheme that is not
//     in the scp-like host:path form, is ReachLocal: it names a place on this
//     machine and no host. A single letter before the colon followed by a
//     slash or a backslash is read as a drive letter and so as a path, which
//     is the one scp-like spelling this reads as local.
//   - Everything else is ReachUnaddressable: a remote with any other scheme,
//     an scp-like remote on any host, a github.com remote naming something
//     other than a repository, and an empty remote.
//
// The residual gap is that a remote in the local shapes may still be a mount
// of something a code host serves; this reads the remote's shape and has no
// way to see past it. The gap points at the safe side for P6, because a local
// remote is still pushed to through the push stage's anchored update.
func ReachOf(remote string) (Reach, string) {
	if specifier, ok := GitHubRepository(remote); ok {
		return ReachHosted, specifier
	}
	if localRemote(strings.TrimSpace(remote)) {
		return ReachLocal, ""
	}
	return ReachUnaddressable, ""
}

// localRemote reports whether remote is in one of the shapes ReachOf reads as
// naming no host.
func localRemote(remote string) bool {
	if remote == "" {
		return false
	}
	if scheme, _, found := strings.Cut(remote, "://"); found && validScheme(scheme) {
		return strings.EqualFold(scheme, "file")
	}
	authority, rest, found := strings.Cut(remote, ":")
	if !found || strings.ContainsAny(authority, `/\`) {
		return true
	}
	return len(authority) == 1 && isASCIILetter(authority[0]) &&
		(strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, `\`))
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
