package gate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/vcs"
)

// RemoteName is the remote a gate is reached by. PRD principle P1 makes
// pushing to a named remote the consent boundary, so this name, and no other,
// is what this package writes into a working copy.
const RemoteName = "assistant"

// Spec describes the gate an operation acts on.
type Spec struct {
	// Home is the assistant home root, per PRD section 8's on-disk layout.
	// Gate repositories live in repos/ beneath it. It must be an absolute
	// path; it does not have to exist yet.
	Home string
	// WorkingPath is the root of the working copy the gate belongs to. It
	// must be an absolute path to an existing directory that is a working
	// copy.
	WorkingPath string
	// Command is the absolute path of the executable the gate's hooks invoke
	// for admission and notification. It is written into the hooks, so a push
	// cannot choose it through PATH. Remove ignores it.
	Command string
}

// Gate is a gate as it stands after an operation on it.
type Gate struct {
	id          string
	repository  string
	workingPath string
	reattached  bool
}

// ID is the gate's identifier. It is the hash of the working copy path the
// gate was first created for, which after a move is no longer the hash of the
// path the gate is bound to now. Identity is what the gate keeps across a
// move, and keeping it is what keeps the run history attached to it.
func (g *Gate) ID() string { return g.id }

// Repository is the absolute path of the gate's bare repository, which is also
// what the working copy's assistant remote points at.
func (g *Gate) Repository() string { return g.repository }

// WorkingPath is the resolved absolute path of the working copy the gate is
// bound to.
func (g *Gate) WorkingPath() string { return g.workingPath }

// Reattached reports that initialization found this working copy already
// naming a gate whose recorded working copy is gone, and bound that existing
// gate to the path it is at now rather than creating a second one.
func (g *Gate) Reattached() bool { return g.reattached }

// Option configures an operation on a gate.
type Option func(*settings)

type settings struct {
	open Opener
}

// WithOpener replaces how a working copy is opened. The default opens it with
// internal/vcs, which is what this product uses. Remove needs an opener whose
// result satisfies Detacher; see that interface for why the default does not.
// A nil opener leaves the default in place.
func WithOpener(open Opener) Option {
	return func(s *settings) {
		if open != nil {
			s.open = open
		}
	}
}

func resolveSettings(opts []Option) settings {
	s := settings{open: openWithVCS}
	for _, opt := range opts {
		opt(&s)
	}
	return s
}

func openWithVCS(ctx context.Context, path string) (WorkingCopy, error) {
	return vcs.OpenWorktree(ctx, path)
}

// Initialize creates the gate for a working copy, or repairs the one it
// already has, and points the working copy's assistant remote at it. It is
// safe to run again: a gate that is already correct is left as it is, and a
// gate that has lost its hooks, its record, or its remote gets them back.
//
// The working copy's other remotes are neither read nor written. An ordinary
// push to origin after this returns behaves exactly as it did before.
//
// Three outcomes are possible and Gate reports which one happened. A working
// copy with no gate gets one at the identifier Identify gives its path. A
// working copy that names a gate whose recorded working copy is gone is
// reattached to that gate, keeping its identifier and everything recorded
// against it. A working copy that names a gate whose recorded working copy is
// still there and still bound to it is a copy of that working copy, and gets
// its own gate rather than sharing the original's.
//
// It refuses with ErrGateClaimed when the gate at the identifier this working
// copy's path hashes to records a different working copy that is still bound
// to it, which is what a working copy placed at the path a moved one left
// behind meets. Taking that gate over would hand this working copy the other
// one's references and rewrite the binding everything recorded against the
// gate rests on.
//
// It refuses with ErrTemplateHooks when the repository it creates is born
// carrying a hook, and with ErrCustomHookConflict when a hook it did not write
// cannot be preserved. Every refusal happens before the working copy's remote
// is written, so a refused initialization leaves the working copy as it was.
func Initialize(ctx context.Context, spec Spec, opts ...Option) (*Gate, error) {
	set := resolveSettings(opts)
	home, workingPath, err := validate(spec, true)
	if err != nil {
		return nil, err
	}
	copyOf, err := set.open(ctx, workingPath)
	if err != nil {
		return nil, fmt.Errorf("gate: opening the working copy at %s: %w", workingPath, err)
	}

	id, err := Identify(workingPath)
	if err != nil {
		return nil, err
	}
	repo := repositoryPath(home, id)
	reattached := false
	existing, ok, err := alreadyNamedGate(ctx, set, home, repo, workingPath, copyOf)
	if err != nil {
		return nil, err
	}
	if ok {
		repo, id, reattached = existing.path, existing.id, existing.moved
	} else if err := ensureOwnGateIsFree(ctx, set, repo, id, workingPath); err != nil {
		return nil, err
	}

	if err := ensureRepository(ctx, repo); err != nil {
		return nil, err
	}
	if err := installHooks(repo, id, spec.Command); err != nil {
		return nil, err
	}
	if err := writeRecord(repo, record{Version: recordVersion, ID: id, WorkingPath: workingPath}); err != nil {
		return nil, err
	}
	if err := copyOf.SetRemote(ctx, RemoteName, repo); err != nil {
		return nil, fmt.Errorf("gate: pointing the %s remote of %s at %s: %w", RemoteName, workingPath, repo, err)
	}
	return &Gate{id: id, repository: repo, workingPath: workingPath, reattached: reattached}, nil
}

// Remove deletes a working copy's gate and gives up the remote that names it.
// The working copy is left usable and every other remote it has, origin
// included, is untouched.
//
// It refuses with ErrDetachUnsupported, before deleting anything, when the
// working copy cannot remove a remote, because a gate whose repository is gone
// and whose remote remains has not been removed. It refuses with ErrNoGate
// when there is no gate to remove, and with ErrNotAGate when the path the
// remote names is not a gate repository of this home, rather than deleting a
// directory it cannot identify.
func Remove(ctx context.Context, spec Spec, opts ...Option) error {
	set := resolveSettings(opts)
	home, workingPath, err := validate(spec, false)
	if err != nil {
		return err
	}
	copyOf, err := set.open(ctx, workingPath)
	if err != nil {
		return fmt.Errorf("gate: opening the working copy at %s: %w", workingPath, err)
	}
	detacher, ok := copyOf.(Detacher)
	if !ok {
		return fmt.Errorf("%w: %T cannot remove the %s remote of %s, so nothing was removed",
			ErrDetachUnsupported, copyOf, RemoteName, workingPath)
	}

	repo, err := boundRepository(ctx, copyOf)
	if err != nil {
		return err
	}
	if repo == "" {
		return fmt.Errorf("%w: %s has no %s remote", ErrNoGate, workingPath, RemoteName)
	}
	if err := verifyGateRepository(home, repo); err != nil {
		return err
	}

	if err := detacher.RemoveRemote(ctx, RemoteName); err != nil && !errors.Is(err, vcs.ErrRemoteNotFound) {
		return fmt.Errorf("gate: removing the %s remote of %s: %w", RemoteName, workingPath, err)
	}
	if err := os.RemoveAll(repo); err != nil {
		return fmt.Errorf("gate: deleting %s: %w", repo, err)
	}
	return nil
}

// validate turns a Spec into the two resolved paths every operation needs.
// requireCommand is false for an operation that writes no hook.
func validate(spec Spec, requireCommand bool) (home, workingPath string, err error) {
	if spec.Home == "" {
		return "", "", fmt.Errorf("%w: home is empty", ErrInvalidSpec)
	}
	if !filepath.IsAbs(spec.Home) {
		return "", "", fmt.Errorf("%w: home %q is not absolute", ErrInvalidSpec, spec.Home)
	}
	home = resolveExisting(spec.Home)

	if !filepath.IsAbs(spec.WorkingPath) {
		return "", "", fmt.Errorf("%w: working path %q is not absolute", ErrInvalidSpec, spec.WorkingPath)
	}
	if !isDirectory(spec.WorkingPath) {
		return "", "", fmt.Errorf("%w: working path %q is not a directory", ErrInvalidSpec, spec.WorkingPath)
	}
	workingPath, err = resolvePath(spec.WorkingPath)
	if err != nil {
		return "", "", err
	}

	if requireCommand {
		if err := validateCommand(spec.Command); err != nil {
			return "", "", err
		}
	}
	return home, workingPath, nil
}

// validateCommand refuses a hook command that a push could reinterpret. It
// must be an absolute path, so that PATH at push time cannot choose what
// admission runs, and it must be something this host can execute, so that an
// initialization does not report success over hooks that fail the first time
// somebody pushes.
func validateCommand(command string) error {
	if command == "" {
		return fmt.Errorf("%w: hook command is empty", ErrInvalidSpec)
	}
	if !filepath.IsAbs(command) {
		return fmt.Errorf("%w: hook command %q is not absolute, so PATH at push time would choose it",
			ErrInvalidSpec, command)
	}
	if strings.ContainsRune(command, 0) {
		return fmt.Errorf("%w: hook command contains a null byte", ErrInvalidSpec)
	}
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Errorf("%w: hook command %q is not executable: %w", ErrInvalidSpec, command, err)
	}
	return nil
}

// claim is an existing gate a working copy already names and may keep.
type claim struct {
	path string
	id   string
	// moved reports that the gate records a different working copy from the
	// one claiming it, so keeping it is a reattachment rather than the
	// ordinary case of a working copy still standing where its gate says.
	moved bool
}

// alreadyNamedGate decides whether the gate a working copy already names is
// the gate this initialization should use.
//
// It answers no in one case only: the gate records a working copy that is
// still there and still names it, which is exactly what a copy of that working
// copy looks like from here. Then the claim on the gate is held by somebody
// else and the caller falls back to a gate of its own.
//
// It answers no without an opinion when the named gate is the one the working
// copy's own path hashes to, because the caller is going to use that gate
// anyway and calling it a reattachment would be wrong.
func alreadyNamedGate(ctx context.Context, set settings, home, own, workingPath string, copyOf WorkingCopy) (claim, bool, error) {
	named, err := boundRepository(ctx, copyOf)
	if err != nil || named == "" || named == own {
		return claim{}, false, err
	}
	if err := verifyGateRepository(home, named); err != nil {
		if errors.Is(err, ErrNotAGate) {
			// A remote pointing at something that is not a gate of this home
			// is not a claim on anything. Initialization proceeds with the
			// working copy's own gate and overwrites the remote.
			return claim{}, false, nil
		}
		return claim{}, false, err
	}
	rec, _, err := readRecord(named)
	if err != nil {
		return claim{}, false, err
	}
	if repositoryPath(home, rec.ID) != named {
		return claim{}, false, fmt.Errorf("%w: %s records identifier %q, which belongs at %s",
			ErrMalformedRecord, named, rec.ID, repositoryPath(home, rec.ID))
	}
	if rec.WorkingPath == workingPath {
		// The gate records this very working copy. It is already attached,
		// which is what an initialization repeated after a move looks like
		// from the second time onwards.
		return claim{path: named, id: rec.ID}, true, nil
	}
	held, err := stillBound(ctx, set, rec.WorkingPath, named)
	if err != nil {
		return claim{}, false, err
	}
	if held {
		return claim{}, false, nil
	}
	return claim{path: named, id: rec.ID, moved: true}, true, nil
}

// ensureOwnGateIsFree decides whether the repository already sitting where
// this working copy's identifier puts it may be adopted.
//
// The identifier is the hash of a path, and a path is not a working copy. A
// working copy that moves away leaves its path free for another one, and the
// gate that path hashes to is by then bound to the moved copy and holding
// everything its runs recorded. Adopting it because the directory happens to
// be there would give the new working copy the moved one's references and take
// the moved one's gate away from it, which is the loss PRD principle P6 is
// about applied to the record of what was validated rather than to a commit.
//
// A repository with no record is adoptable: that is a gate whose record was
// lost, and repairing it is ordinary. A record that will not read is not, for
// the same reason a record naming somebody else is not, because the binding it
// carries is exactly the fact that cannot then be established.
func ensureOwnGateIsFree(ctx context.Context, set settings, repo, id, workingPath string) error {
	rec, exists, err := readRecord(repo)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if rec.ID != id {
		return fmt.Errorf("%w: %s records identifier %q, but its name says %q",
			ErrMalformedRecord, repo, rec.ID, id)
	}
	if rec.WorkingPath == workingPath {
		return nil
	}
	held, err := stillBound(ctx, set, rec.WorkingPath, repo)
	if err != nil {
		return err
	}
	if !held {
		// The working copy the gate records is gone and this one hashes to the
		// same identifier, so there is nobody left to take the gate from.
		return nil
	}
	return fmt.Errorf("%w: the gate at %s belongs to the working copy at %s, which still points at it, "+
		"and %s asks for the same gate because both paths hash to %s; "+
		"remove the gate of %s, or move %s somewhere else, before initializing it",
		ErrGateClaimed, repo, rec.WorkingPath, workingPath, id, rec.WorkingPath, workingPath)
}

// stillBound reports whether the working copy a gate records is still there
// and still names that gate. A working copy that moved leaves nothing behind,
// so the gate is free; a working copy that was copied leaves the original
// behind, so the gate is not.
//
// Anything that cannot be established counts as not bound, and which way that
// fails is a decision rather than an accident. Treating an unreadable claimant
// as still bound would give a moved working copy a second gate and orphan the
// run history recorded against the first, which is the loss PRD principle P6
// is about. Treating it as free costs a copy the original's gate only once the
// original has become unopenable, at which point the original has lost it
// either way.
func stillBound(ctx context.Context, set settings, claimant, repo string) (bool, error) {
	if !isDirectory(claimant) {
		return false, nil
	}
	resolved, err := resolvePath(claimant)
	if err != nil {
		return false, nil //nolint:nilerr // a claimant that will not resolve holds nothing
	}
	other, err := set.open(ctx, resolved)
	if err != nil {
		return false, nil //nolint:nilerr // a claimant that will not open holds nothing
	}
	named, err := boundRepository(ctx, other)
	if err != nil {
		return false, err
	}
	return named == repo, nil
}

// boundRepository returns the path a working copy's assistant remote names,
// cleaned, or the empty string when it has no such remote.
func boundRepository(ctx context.Context, copyOf WorkingCopy) (string, error) {
	url, err := copyOf.RemoteURL(ctx, RemoteName)
	if err != nil {
		if errors.Is(err, vcs.ErrRemoteNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("gate: reading the %s remote: %w", RemoteName, err)
	}
	url = strings.TrimSpace(url)
	if url == "" || !filepath.IsAbs(url) {
		// A gate is always a local path this package wrote. Anything else is
		// not a gate, whoever put it there.
		return "", nil
	}
	return filepath.Clean(url), nil
}

// verifyGateRepository refuses a path that is not a gate repository of this
// home. It is what stands between Remove and deleting a directory only because
// a remote pointed at it.
func verifyGateRepository(home, repo string) error {
	if filepath.Dir(repo) != repositoriesDir(home) || !strings.HasSuffix(repo, ".git") {
		return fmt.Errorf("%w: %s is not in %s", ErrNotAGate, repo, repositoriesDir(home))
	}
	_, exists, err := readRecord(repo)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %s carries no %s", ErrNotAGate, repo, recordName)
	}
	return nil
}

// ensureRepository creates the gate's bare repository when it is not there and
// leaves it alone when it is.
//
// A repository this package creates has to be born with no hooks. What can
// still put one there is a git template chosen by configuration outside this
// process, which internal/vcs documents that it cannot close; see doc.go. A
// hook in a repository that did not exist a moment ago arrived by that route,
// so it is refused, and the repository is deleted again so that the next
// attempt does not meet the same hook as an established one and preserve it.
// The same file in a repository that already existed is somebody's own hook
// and is preserved.
func ensureRepository(ctx context.Context, repo string) error {
	parent := filepath.Dir(repo)
	// The assistant home holds every gated project's whole history, alongside
	// the lock, the socket, and the database PRD section 8 puts there, so the
	// directories this package creates under it are the owner's alone.
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("gate: creating %s: %w", parent, err)
	}
	creating := !isDirectory(repo) || isEmptyDirectory(repo)
	if _, err := vcs.InitBare(ctx, repo); err != nil {
		return fmt.Errorf("gate: creating the gate repository at %s: %w", repo, err)
	}
	if !creating {
		return nil
	}
	born, err := activeHooks(repo)
	if err != nil {
		return err
	}
	if len(born) == 0 {
		return nil
	}
	if err := os.RemoveAll(repo); err != nil {
		return fmt.Errorf("gate: deleting the repository created at %s carrying template hooks: %w", repo, err)
	}
	return fmt.Errorf("%w: %s was created holding %s, chosen by an init.templateDir this process cannot see past",
		ErrTemplateHooks, repo, strings.Join(born, ", "))
}
