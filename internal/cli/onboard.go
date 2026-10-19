package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/vcs"
)

// onboardRefused is init's refusal during inspection, before a gate or a
// repository record is written. The reason is the typed half a caller branches
// on; keys, set only for a configuration conflict, names the command keys that
// disagree and is not printed, because those names are the configuration
// schema's and a person reads the row instead. The cause is what was read,
// when there is one, so errors.Is on exec.ErrNotFound survives a missing git.
type onboardRefused struct {
	reason machine.OnboardRefusal
	keys   []string
	cause  error
}

// Error is the row a person reads. The keys that disagree stay on the value
// for a caller in this package; the row says the two copies disagree and does
// not spell the schema's names.
func (e onboardRefused) Error() string { return machine.OnboardRefusalPhrase(e.reason).String() }

// Unwrap resolves to what was read.
func (e onboardRefused) Unwrap() error { return e.cause }

// missingGit returns OnboardGitMissing when err is a git binary that could
// not be started, and err otherwise. A caller handles the refusal; a failure
// that is not a missing binary is left as it was.
func missingGit(err error) error {
	if err != nil && errors.Is(err, exec.ErrNotFound) {
		return onboardRefused{reason: machine.OnboardGitMissing, cause: err}
	}
	return err
}

// worktreeFailure is how a failure to open the working copy is returned.
// A git binary that cannot be started is OnboardGitMissing, which a caller
// handles; every other failure stays the sentence that names the directory.
func worktreeFailure(dir string, err error) error {
	if refused := missingGit(err); refused != err {
		return refused
	}
	return fmt.Errorf("%s is not inside a working copy: %w", dir, err)
}

// ambiguousOrigin refuses when origin's fetch address and push address
// differ. init is about to record one address as where changes go, and the
// two reads are both origin, so picking either would be a guess. It reads
// the configured push address and contacts nothing. A read that fails is
// returned as that failure, including a missing git, and is not reported as
// ambiguity.
func ambiguousOrigin(ctx context.Context, repo *vcs.Repository, fetch string) error {
	push, err := repo.PushURL(ctx, "origin")
	if err != nil {
		if refused := missingGit(err); refused != err {
			return refused
		}
		return fmt.Errorf("reading where this working copy's origin pushes: %w", err)
	}
	if push != fetch {
		return onboardRefused{reason: machine.OnboardIdentityAmbiguous}
	}
	return nil
}

// commandKeysCompared are the command keys init offers. A disagreement on
// one of them, between the checkout's configuration file and the copy the
// local default branch commit holds, is OnboardConfigConflict. A key outside
// this list is not compared: init does not offer it, and a difference there
// is not this refusal.
var commandKeysCompared = []config.Key{config.KeyCommandsTest, config.KeyCommandsLint, config.KeyCommandsDocument}

// conflictingCommands refuses when the checkout's configuration file and the
// copy on the default branch disagree on a command key init offers.
//
// Both copies are read and neither is written. The branch copy is the file
// at branch in the local repository, not a fresh fetch of origin: a local
// branch behind origin can agree with the checkout while origin's copy does
// not, and this check does not see that. A branch document that is absent is
// not a conflict, which is what lets init offer commands into a file that
// has not been committed yet. A copy that does not parse, or a committed
// path that cannot be read as a file, is not this refusal either: the offer
// still reports a checkout file it cannot read, and a run refuses a trusted
// document it cannot parse on its own terms. This function returns nil in
// those cases rather than guessing that the two copies disagree.
func conflictingCommands(ctx context.Context, working, branch string) error {
	repo, err := vcs.OpenWorktree(ctx, working, vcs.WithRedactor(redact.New()))
	if err != nil {
		if refused := missingGit(err); refused != err {
			return refused
		}
		return err
	}
	committed, err := repo.FileAt(ctx, branch, config.RepositoryDocument)
	if errors.Is(err, vcs.ErrPathNotFound) {
		return nil
	}
	if err != nil {
		return nil
	}
	committedSet, err := commandSettings(committed)
	if err != nil {
		return nil
	}
	observed, err := os.ReadFile(filepath.Join(working, config.RepositoryDocument))
	checkout := map[config.Key]string{}
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil
	default:
		checkout, err = commandSettings(observed)
		if err != nil {
			return nil
		}
	}
	var keys []string
	for _, key := range commandKeysCompared {
		cv, cok := committedSet[key]
		wv, wok := checkout[key]
		if cok != wok || cv != wv {
			keys = append(keys, string(key))
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return onboardRefused{reason: machine.OnboardConfigConflict, keys: keys}
}

// commandSettings is which of commandKeysCompared data sets, and the command
// each one names. A document that does not parse is an error, which the
// caller treats as "not compared" rather than as a conflict.
func commandSettings(data []byte) (map[config.Key]string, error) {
	layer, err := config.Parse(config.OriginTrusted, data)
	if err != nil {
		return nil, err
	}
	resolved, err := config.Resolve(config.Absent(config.OriginGlobal), layer)
	if err != nil {
		return nil, err
	}
	values := map[config.Key]string{
		config.KeyCommandsTest:     resolved.Config.Commands.Test,
		config.KeyCommandsLint:     resolved.Config.Commands.Lint,
		config.KeyCommandsDocument: resolved.Config.Commands.Document,
	}
	out := map[config.Key]string{}
	for _, key := range commandKeysCompared {
		if layer.IsSet(key) {
			out[key] = values[key]
		}
	}
	return out, nil
}
