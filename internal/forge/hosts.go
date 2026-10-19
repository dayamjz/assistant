package forge

import "strings"

// Hosts is every code host this build addresses, one Host per host, and it is
// itself a Host: Open and OpenFork hand a specifier to the member that
// addresses it. It is the one place that decides which adapter a specifier
// reaches, so a stage body, the service's own reads and a cleanup proof all
// open through the same choice and none of them carries a second one.
//
// The choice is read off the specifier and nothing else. RepositoryOf writes
// a GitLab specifier with GitLabHostname in front of it and a GitHub one as
// owner/name, so a specifier GitLabRepository wrote goes to GitLab and every
// other one goes to GitHub, whose Open refuses anything that is not
// owner/name. No configuration key, environment variable or working copy
// takes part, so a repository document cannot point a run at a host its
// record does not name.
//
// A member left nil is a host this value cannot open on, and a specifier
// naming that host is refused rather than handed to the other member.
//
// It is safe for concurrent use when its members are, which every Host must
// be.
type Hosts struct {
	// GitHub opens owner/name specifiers.
	GitHub Host
	// GitLab opens gitlab.com/ specifiers.
	GitLab Host
}

var _ Host = Hosts{}

// Open returns the Provider for repository from the member that addresses
// it, on Host.Open's terms. An empty specifier is refused here, before any
// member is chosen, for the reason GitHubHost.Open gives.
func (h Hosts) Open(repository string) (Provider, error) {
	if repository == "" {
		return nil, &argumentError{
			what:   "repository",
			reason: "is required, because a provider that resolves one for itself would address a repository this run never named",
		}
	}
	host, err := h.member(repository)
	if err != nil {
		return nil, err
	}
	return host.Open(repository)
}

// OpenFork returns the Provider for repository whose pull requests are
// proposed from fork, on Host.OpenFork's terms. The two must name the same
// host: a pull request is proposed between two repositories on one code
// host, so a fork on another host is refused here rather than handed to
// either member.
func (h Hosts) OpenFork(repository, fork string) (Provider, error) {
	if repository == "" {
		return nil, &argumentError{
			what:   "repository",
			reason: "is required, because a provider that resolves one for itself would address a repository this run never named",
		}
	}
	if fork == "" {
		return nil, &argumentError{
			what:   "head repository",
			reason: "is required, because a pull request from a fork this run did not name would be proposed from a repository nobody chose",
		}
	}
	if isGitLabSpecifier(repository) != isGitLabSpecifier(fork) {
		return nil, &argumentError{
			what:   "head repository",
			reason: "names a repository on a different code host than the one the pull request is proposed to",
		}
	}
	host, err := h.member(repository)
	if err != nil {
		return nil, err
	}
	return host.OpenFork(repository, fork)
}

// member is the Host that addresses spec, or a refusal naming the host this
// value holds no member for.
func (h Hosts) member(spec string) (Host, error) {
	host, name := h.GitHub, GitHubHostname
	if isGitLabSpecifier(spec) {
		host, name = h.GitLab, GitLabHostname
	}
	if host == nil {
		return nil, &Refusal{
			Reason: ReasonUnavailable,
			Op:     "open",
			Detail: "this build was given no adapter for " + name + ", so nothing was opened",
		}
	}
	return host, nil
}

// isGitLabSpecifier reports whether spec names GitLabHostname the way
// GitLabRepository writes it. A specifier that only resembles one is still
// routed to GitLab, whose Open is what refuses it, so a malformed GitLab
// specifier never reaches the GitHub adapter.
func isGitLabSpecifier(spec string) bool {
	return strings.HasPrefix(spec, gitLabPrefix)
}
