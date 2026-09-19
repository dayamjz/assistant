package forge

import (
	"os/exec"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Host is a code host an adapter is opened against one repository on.
//
// It exists because a Provider addresses exactly one repository and the thing
// that holds a Provider does not. A service resolves its adapters once and
// then serves every run, so a Provider settled at that point would address one
// repository for every run of that service, which is the failure this seam is
// shaped to make unsayable: a stage cannot reach a Provider without naming the
// repository it is for.
//
// The split is by lifetime. Everything settled once - the provider command
// line, the environment it runs in, the redactor, the output bound - belongs
// to the Host and is fixed when the service builds it. The repository is the
// run's own fact and arrives here, from the run's record.
//
// An implementation must be safe for concurrent use, and so must every
// Provider it returns.
type Host interface {
	// Open returns the Provider addressing repository, which is the specifier
	// the host names repositories by; for GitHub that is owner/name.
	//
	// It refuses a repository it cannot address, including an empty one. A
	// caller with no specifier is a run whose record does not say which
	// repository on this host it is for, and answering that with a Provider
	// resolving one from somewhere else is how a run acts on a repository
	// nobody chose.
	//
	// It performs no request. Whether the repository exists, and whether the
	// specifier still names it, are answers the Provider's own calls carry.
	Open(repository string) (Provider, error)
}

// GitHubHost is the Host that opens GitHub adapters. It holds what NewGitHub
// takes apart from the repository, so the adapter for a run differs from the
// adapter for another run in that one field and nothing else.
//
// It is safe for concurrent use: its fields are fixed at construction and Open
// builds a new adapter rather than sharing one.
type GitHubHost struct {
	redact vcs.Redactor
	opts   []Option
}

var _ Host = (*GitHubHost)(nil)

// NewGitHubHost returns the Host that opens GitHub adapters through gh.
//
// redact and opts are what every adapter this host opens is constructed with,
// on the same terms NewGitHub states, and redact is required there for the
// reason given there.
//
// An option naming a repository is accepted and then overridden, because Open
// applies the repository it was given last. A host with a repository fixed in
// its options would open the same repository for every run, which is the
// lifetime mistake this type exists to prevent, so it cannot be made by
// passing one here.
func NewGitHubHost(redact vcs.Redactor, opts ...Option) *GitHubHost {
	return &GitHubHost{redact: redact, opts: append([]Option(nil), opts...)}
}

// Open returns the GitHub adapter addressing repository, as owner/name.
//
// An empty specifier is refused here rather than passed on, because NewGitHub
// accepts an adapter that names only a working directory and this host never
// opens one: an adapter that resolved its repository from a directory is
// exactly what Host exists to keep a run away from.
func (h *GitHubHost) Open(repository string) (Provider, error) {
	if repository == "" {
		return nil, &argumentError{
			what:   "repository",
			reason: "is required, because a provider that resolves one for itself would address a repository this run never named",
		}
	}
	opts := make([]Option, 0, len(h.opts)+1)
	opts = append(opts, h.opts...)
	opts = append(opts, WithRepository(repository))
	adapter, err := NewGitHub(h.redact, opts...)
	if err != nil {
		// The refusal is returned without the adapter beside it. NewGitHub
		// answers a refusal with a nil *GitHub, and returning that as a
		// Provider would hand a caller an interface value that is not nil and
		// panics on first use, so the refusing path names no provider at all.
		return nil, err
	}
	return adapter, nil
}

// Probe reports whether the provider command line this host would run
// resolves here, and to what. It reads the same settled command line every
// adapter Open builds runs - the executable WithBinary named, or
// DefaultGitHubBinary through PATH - so its answer is about the invocation a
// run would make rather than a second spelling of it.
//
// What it establishes is exactly one thing: that the command line resolves to
// a file this process may execute, which is the requirement whose absence
// every call otherwise reports as "the provider command line could not be
// run". It runs nothing, on the same trade internal/agents makes for an
// agent's availability: proving more would cost a process and a possible hang
// at the moment a caller is deciding what can run here. So it deliberately
// does not establish that the executable starts, that it behaves as a
// provider, that it is authenticated, or that any repository is reachable;
// each of those surfaces as its own refusal from the call that needs it, and
// an executable that resolves and then fails to start is a gap this probe
// does not close. Nor is the answer a lock: it is what resolves as it is
// asked, and each invocation resolves again.
//
// A command line that resolves is returned as the resolved path. One that
// does not is a *Refusal with ReasonUnavailable. A caller reporting the
// refusal must not gate a run on it: a run can start without a code host, and
// the stages that talk to one refuse there.
func (h *GitHubHost) Probe() (string, error) {
	s := newSettings(h.opts)
	resolved, err := exec.LookPath(s.bin)
	if err != nil {
		return "", &Refusal{
			Reason: ReasonUnavailable,
			Op:     "probe",
			Detail: h.redact.Redact("the provider command line could not be run: " + err.Error()),
			Cause:  err,
		}
	}
	return resolved, nil
}
