package forge

import (
	"path/filepath"
	"strings"
)

// RemoteKind is how this build treats a git remote for pull requests and
// checks. It is the trichotomy issue #283 settled: a host this adapter
// addresses, a local path with no code host, or a network host this build
// cannot talk to.
type RemoteKind int

const (
	// RemoteGitHub is a remote on GitHubHostname that GitHubRepository reads.
	RemoteGitHub RemoteKind = iota
	// RemoteLocal is a local path or a file:// remote: there is no code host
	// to open a pull request on. A run of such a repository skips the pull
	// request and checks stages after a successful push.
	RemoteLocal
	// RemoteUnsupported is a remote that names a network host this build does
	// not address (GitLab, GitHub Enterprise, an scp-style host that is not
	// github.com). A run against one is refused at start rather than walked
	// to the pull request stage and stalled.
	RemoteUnsupported
)

// String names the kind for logs and refusals.
func (k RemoteKind) String() string {
	switch k {
	case RemoteGitHub:
		return "github"
	case RemoteLocal:
		return "local"
	case RemoteUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// ClassifyRemote reports how this build treats remote for pull requests and
// checks. remote is a git remote URL as internal/store holds it.
//
// The three answers are exhaustive and mutually exclusive: a github.com
// repository this adapter addresses, a local path or file:// remote with no
// code host, or a network host this build cannot talk to. An empty remote is
// unsupported rather than local, because there is nothing to push to.
//
// A bare name with no slash (for example "remote.git") is local: git accepts
// that shape as a path, and this classifier does not invent a host for it.
// A scheme-less host/path form (for example "github.com/org/repo" or
// "gitlab.com/org/repo") is unsupported rather than local: treating it as
// no-code-host would end a run as pushed while claiming there is no code host
// for a remote the operator clearly meant as one. Use an https://, ssh://,
// file://, scp-like, or explicit filesystem path instead.
func ClassifyRemote(remote string) RemoteKind {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return RemoteUnsupported
	}
	if _, ok := GitHubRepository(remote); ok {
		return RemoteGitHub
	}
	if scheme, rest, found := strings.Cut(remote, "://"); found {
		if strings.EqualFold(scheme, "file") {
			return RemoteLocal
		}
		// A non-file scheme names a network resource this build does not
		// address (GitLab, GHE, https to another host).
		_ = rest
		return RemoteUnsupported
	}
	if localPath(remote) {
		return RemoteLocal
	}
	if host, _, ok := splitRemote(remote); ok && host != "" {
		return RemoteUnsupported
	}
	if hostLookingPath(remote) {
		// github.com/org/repo is not a GitHub remote this adapter reads
		// (GitHubRepository requires a scheme or scp-like form), and it is
		// not a filesystem path. Refuse rather than call it local.
		return RemoteUnsupported
	}
	// A bare relative path with no scheme, no host:path form, and no slash
	// after a DNS-looking first segment (for example "remote.git").
	return RemoteLocal
}

// localPath reports whether remote is a filesystem path rather than a
// host:path remote. Absolute paths, ./ and ../ relatives, and a Windows drive
// letter (C:\…) count; an scp-style host:path does not.
func localPath(remote string) bool {
	if filepath.IsAbs(remote) {
		return true
	}
	if strings.HasPrefix(remote, "./") || strings.HasPrefix(remote, "../") || remote == "." || remote == ".." {
		return true
	}
	// Windows drive: a single letter, a colon, then a path separator or end.
	if len(remote) >= 2 && remote[1] == ':' {
		drive := remote[0]
		if (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z') {
			if len(remote) == 2 || remote[2] == '\\' || remote[2] == '/' {
				return true
			}
		}
	}
	return false
}

// hostLookingPath reports whether remote looks like a scheme-less host/path
// remote (github.com/org/repo): a slash-separated form whose first segment
// contains a dot the way a DNS name does. Bare names without a slash are not
// this shape, so "remote.git" stays a local path.
func hostLookingPath(remote string) bool {
	host, path, found := strings.Cut(remote, "/")
	if !found || host == "" || path == "" {
		return false
	}
	if !strings.Contains(host, ".") {
		return false
	}
	// A path segment that is not a host: spaces, backslashes, or a leading
	// dot that would be an odd DNS label for a remotes we care about.
	if strings.ContainsAny(host, " \\") || strings.HasPrefix(host, ".") {
		return false
	}
	return true
}
