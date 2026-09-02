package vcs

import (
	"context"
	"errors"
	"strings"
)

// FetchSpec describes one fetch.
type FetchSpec struct {
	// Remote is a configured remote name or a URL. It is required.
	Remote string
	// Refspecs are the refspecs to fetch. With none, git uses the remote's
	// configured refspec, which a URL does not have, so a fetch from a URL
	// with no refspec updates no reference.
	Refspecs []string
	// Prune removes remote-tracking references the remote no longer
	// advertises.
	Prune bool
	// Tags fetches the remote's tags in addition to the refspecs. With it
	// false, no tag is fetched, not even one pointing into fetched history.
	Tags bool
}

// Fetch updates this repository from a remote. It writes references and
// objects and nothing else: it does not merge, rebase, or move HEAD.
//
// It never writes FETCH_HEAD, so two fetches into one repository do not race
// over that file. A caller that needs to know what the remote holds reads
// RemoteRefs or ListRefs rather than FETCH_HEAD.
//
// Submodule recursion is disabled. A submodule configuration on the branch
// being fetched would otherwise choose further URLs to contact, and PRD
// principle P7 does not let the branch under validation choose what runs.
func (r *Repository) Fetch(ctx context.Context, spec FetchSpec) error {
	if err := checkArg("remote", spec.Remote); err != nil {
		return err
	}
	args := []string{"fetch", "--quiet", "--no-recurse-submodules", "--no-write-fetch-head"}
	if spec.Prune {
		args = append(args, "--prune")
	}
	if spec.Tags {
		args = append(args, "--tags")
	} else {
		args = append(args, "--no-tags")
	}
	args = append(args, "--end-of-options", spec.Remote)
	for _, rs := range spec.Refspecs {
		if err := checkArg("refspec", rs); err != nil {
			return err
		}
		args = append(args, rs)
	}
	_, err := r.run(ctx, "fetch", args...)
	return err
}

// RemoteURL returns the fetch URL configured for a remote, and
// ErrRemoteNotFound when there is no such remote.
//
// The URL is returned exactly as it is configured, including any credentials
// in it. This is the one place in this package that hands a caller a
// credentialed URL, because recovering that URL is what it is for. Everything
// that persists or reports it is responsible for redacting it first.
func (r *Repository) RemoteURL(ctx context.Context, name string) (string, error) {
	if err := checkArg("remote", name); err != nil {
		return "", err
	}
	// Exit status 2 is what git remote reports for a remote it does not have.
	out, code, err := r.runExpecting(ctx, "remote-url", []int{2}, "remote", "get-url", "--", name)
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(string(out))
	if code != 0 || url == "" {
		return "", ErrRemoteNotFound
	}
	return url, nil
}

// SetRemote points a remote at a URL, adding it when it does not exist and
// changing its URL when it does. Running it again with the same arguments
// changes nothing, so an initialization repeated to repair a home does not
// fail on a remote that is already correct.
//
// It sets only the named remote's URL. It never touches another remote, which
// is what keeps PRD principle P1 true: a person's own origin behaves exactly
// as it always has.
func (r *Repository) SetRemote(ctx context.Context, name, url string) error {
	if err := checkArg("remote", name); err != nil {
		return err
	}
	if err := checkArg("url", url); err != nil {
		return err
	}
	_, err := r.RemoteURL(ctx, name)
	switch {
	case err == nil:
		_, err = r.run(ctx, "remote-set-url", "remote", "set-url", "--", name, url)
		return err
	case errors.Is(err, ErrRemoteNotFound):
		_, err = r.run(ctx, "remote-add", "remote", "add", "--", name, url)
		return err
	default:
		return err
	}
}
