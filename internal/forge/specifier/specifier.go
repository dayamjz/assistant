// Package specifier is the grammar of the text internal/forge names a
// repository on a code host by, and nothing else: which hosts a specifier
// may name, and which strings are a specifier for each. It reads no remote,
// opens nothing and talks to no code host; internal/forge's GitHubRepository
// and GitLabRepository are what write a specifier out of a remote, and they
// write only what this package accepts.
//
// It is its own package so that a specifier has one grammar while two
// packages ask it: internal/forge, which routes and opens by it, and
// internal/config, whose autonomy.merge decoder refuses an entry no run
// could ever carry. internal/forge imports internal/config, so the grammar
// cannot live in the first and be read by the second.
//
// A GitHub specifier is owner/name and a GitLab one names its host,
// gitlab.com/group/project, and the two forms are disjoint: a GitHub
// specifier has exactly two segments and a GitLab one at least three, so no
// string is both, and no GitHub specifier equals a GitLab one whether letter
// case is ignored or not.
package specifier

import "strings"

// GitHubHostname is the code host internal/forge's GitHub adapter
// addresses; internal/forge's constant of the same name says what depends
// on it.
const GitHubHostname = "github.com"

// GitLabHostname is the code host internal/forge's GitLab adapter
// addresses; internal/forge's constant of the same name says what depends
// on it.
const GitLabHostname = "gitlab.com"

// GitLabPrefix is what every GitLab specifier begins with, written exactly.
const GitLabPrefix = GitLabHostname + "/"

// GitHub reports whether spec is a GitHub specifier: owner/name, each a
// segment Segment accepts, and nothing else. A scheme, userinfo, a host, or
// a third path segment all fail it.
func GitHub(spec string) bool {
	owner, name, ok := strings.Cut(spec, "/")
	if !ok {
		return false
	}
	return Segment(owner) && Segment(name)
}

// GitLab returns the project path a GitLab specifier names, and reports
// whether spec is one: GitLabPrefix, written exactly, and a path
// GitLabProjectPath accepts. The host is compared exactly rather than
// ignoring letter case, because internal/forge's GitLabRepository writes it
// as GitLabHostname and nothing else writes one.
func GitLab(spec string) (string, bool) {
	path, found := strings.CutPrefix(spec, GitLabPrefix)
	if !found || !GitLabProjectPath(path) {
		return "", false
	}
	return path, true
}

// GitLabProjectPath reports whether path is a GitLab project path: two or
// more segments, each one Segment accepts and none of them "." or "..". One
// segment names a user or a group and not a project, so it is refused.
func GitLabProjectPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || !Segment(part) {
			return false
		}
	}
	return true
}

// Segment reports whether seg is one path segment of a specifier: one to a
// hundred letters, digits, dots, underscores and dashes, not beginning with
// a dash, so a segment is never read as an option on a command line.
func Segment(seg string) bool {
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
