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

// Reattached reports that this initialization bound the working copy to a gate
// that its current path does not hash to. The gate was found through the
// working copy's assistant remote, still filed under the identifier it was
// created with, and its record did not already name this working copy, either
// because it names one that has let go of the gate or because the record is
// gone. Keeping that gate is what carried its identifier, and everything
// recorded against that identifier, across the move.
//
// It reports false for a gate the current path does hash to, which includes a
// working copy that moved away and back again, and false on every later
// initialization of a gate whose record already names its working copy.
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
// gate that has lost its hooks or its record gets them back.
//
// Repair reaches a gate two ways, and only two: the assistant remote in the
// working copy, and the identifier the working copy's current path hashes to.
// A gate that answers to neither cannot be found from here, whether because
// its working copy moved and then lost the remote naming it, or because the
// repository the remote names is no longer on disk. Initialization then
// creates a new gate at the current path's identifier and says so through
// Gate rather than reporting a repair it did not perform.
//
// A remote naming a repository that is gone and a repository whose record is
// gone are not the same state. The second still holds everything its runs
// recorded and is repaired in place; the first holds nothing, so there is
// nothing to keep and the identifier it was filed under is not carried
// forward.
//
// The working copy's other remotes are neither read nor written. An ordinary
// push to origin after this returns behaves exactly as it did before.
//
// Three outcomes are possible and Gate reports which one happened. A working
// copy with no gate gets one at the identifier Identify gives its path. A
// working copy that names a gate whose record does not name it back, because
// the recorded working copy is gone or because the record itself is gone, is
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
// It refuses with ErrTemplateHooks when a hook arrives in the gate during the
// initialization rather than from this package, whether that initialization
// created the repository or repaired one that was already there, and with
// ErrCustomHookConflict when a hook it did not write cannot be preserved.
// Every refusal happens before the working copy's remote is written, so a
// refused initialization leaves the working copy as it was.
//
// A gate taken over while it carried no record is bound on the strength of a
// remote alone. Initialize records that, and Remove refuses to delete a gate
// bound that way; see ErrGateBindingInferred. An initialization whose own path
// hashes to the gate records an evidenced binding instead, including over a
// record left by an earlier inferred one, because standing where the gate is
// named for is evidence a copy cannot produce.
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
	reattached, adopted := false, false
	existing, ok, err := alreadyNamedGate(ctx, set, home, repo, workingPath, copyOf)
	if err != nil {
		return nil, err
	}
	if ok {
		repo, id, reattached, adopted = existing.path, existing.id, existing.moved, existing.adopted
	} else if adopted, err = ownGateBinding(ctx, set, repo, id, workingPath); err != nil {
		return nil, err
	}

	if err := ensureRepository(ctx, repo); err != nil {
		return nil, err
	}
	if err := installHooks(repo, id, spec.Command); err != nil {
		return nil, err
	}
	if err := writeRecord(repo, record{
		Version:     recordVersion,
		ID:          id,
		WorkingPath: workingPath,
		Adopted:     adopted,
	}); err != nil {
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
// remote names is not a gate repository of this home, holds nothing, or
// carries no record, rather than deleting a directory it cannot identify.
// Initializing the working copy is what resolves the last two, and the message
// says so.
//
// It refuses with ErrGateClaimed when the gate the remote names records a
// different working copy that is still pointing at it, which is what a copy of
// a gated project meets: the copy inherited the original's configuration, so
// its remote names a gate that is not its own. Nothing is removed, not even
// the copy's remote, because a removal that acted on somebody else's gate on
// the strength of an inherited remote is the loss PRD principle P6 forbids and
// is the one this package cannot undo.
//
// It refuses with ErrGateBindingInferred, and again removes nothing, when the
// gate's record says the binding was established by taking over a repository
// that carried no record. A record naming this working copy is ordinarily what
// makes a removal safe, and a record this package wrote about a gate nothing
// established was this working copy's is not that.
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
	rec, holds, err := gateRepository(home, repo)
	if err != nil {
		return err
	}
	switch holds {
	case noRepository:
		return fmt.Errorf("%w: the %s remote of %s names %s, and nothing is there; "+
			"initialize %s to get a gate of its own, which repoints the remote, and remove that",
			ErrNotAGate, RemoteName, workingPath, repo, workingPath)
	case repositoryWithoutRecord:
		return fmt.Errorf("%w: %s carries no %s, so nothing there says whose gate it is; "+
			"initialize %s, which writes the record back, and remove it after that",
			ErrNotAGate, repo, recordName, workingPath)
	case repositoryWithRecord:
	}
	if err := ensureAvailableTo(ctx, set, repo, rec, workingPath, fmt.Sprintf(
		"nothing was removed; a working copy copied from %s inherits its %s remote, so if this is such a copy, "+
			"removing the %s remote here detaches it and always succeeds, and the gate itself is removed from %s",
		rec.WorkingPath, RemoteName, RemoteName, rec.WorkingPath)); err != nil {
		return err
	}
	if rec.Adopted {
		return fmt.Errorf("%w: %s was bound to the working copy at %s by taking over a repository that carried no record, "+
			"so only a %s remote ever said the gate was its, and a copy of a gated project inherits that remote too; "+
			"nothing was removed. Removing the %s remote here detaches this working copy and always succeeds, and "+
			"initializing it afterwards gives it a gate of its own; %s is then named by nothing and is yours to delete "+
			"once you are satisfied the history in it is not another working copy's",
			ErrGateBindingInferred, repo, rec.WorkingPath, RemoteName, RemoteName, repo)
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
	// adopted is what record.Adopted will say about the binding this claim
	// establishes: true when this claim takes over a gate that carried no
	// record, and otherwise whatever the record it found already said.
	adopted bool
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
//
// A named gate carrying no record at all is kept rather than left behind. The
// record is what binds a gate to a working copy, and a gate that lost it would
// otherwise be abandoned for a new empty one at the current path's hash, with
// everything recorded against the old identifier unreachable, because nothing
// here scans the home for a gate nobody names. The identifier then comes from
// the directory the gate is filed under, which is where it came from in the
// first place. What that costs is stated in doc.go: with no record there is no
// evidence of ownership left to check, so a copy that inherited the remote can
// take a recordless gate over, and the original meets ErrGateClaimed the next
// time it initializes rather than losing anything quietly.
func alreadyNamedGate(ctx context.Context, set settings, home, own, workingPath string, copyOf WorkingCopy) (claim, bool, error) {
	named, err := boundRepository(ctx, copyOf)
	if err != nil || named == "" || named == own {
		return claim{}, false, err
	}
	rec, holds, err := gateRepository(home, named)
	if err != nil {
		if errors.Is(err, ErrNotAGate) {
			// A remote pointing at something that is not a gate of this home
			// is not a claim on anything. Initialization proceeds with the
			// working copy's own gate and overwrites the remote.
			return claim{}, false, nil
		}
		return claim{}, false, err
	}
	if holds == noRepository {
		// The remote names a gate that is not there. There is nothing to keep
		// and nothing to carry across a move, so this is a gate that answers
		// to neither route and the caller creates one at the working copy's
		// own identifier rather than reporting a repair it did not perform.
		return claim{}, false, nil
	}
	if holds == repositoryWithoutRecord {
		id := identifierAt(home, named)
		if id == "" {
			return claim{}, false, nil
		}
		return claim{path: named, id: id, moved: true, adopted: true}, true, nil
	}
	if repositoryPath(home, rec.ID) != named {
		return claim{}, false, fmt.Errorf("%w: %s records identifier %q, which belongs at %s",
			ErrMalformedRecord, named, rec.ID, repositoryPath(home, rec.ID))
	}
	available, err := availableTo(ctx, set, named, rec, workingPath)
	if err != nil || !available {
		return claim{}, false, err
	}
	// A record that already names this working copy is the ordinary case an
	// initialization repeated after a move reaches from the second time
	// onwards, and is not a reattachment.
	return claim{
		path:    named,
		id:      rec.ID,
		moved:   rec.WorkingPath != workingPath,
		adopted: rec.Adopted,
	}, true, nil
}

// ownGateBinding decides whether the repository already sitting where this
// working copy's identifier puts it may be adopted, and reports what the
// record it carries already says about how its binding was established.
//
// The identifier is the hash of a path, and a path is not a working copy. A
// working copy that moves away leaves its path free for another one, and the
// gate that path hashes to is by then bound to the moved copy and holding
// everything its runs recorded. Adopting it because the directory happens to
// be there would give the new working copy the moved one's references and take
// the moved one's gate away from it, which is the loss PRD principle P6 is
// about applied to the record of what was validated rather than to a commit.
//
// A repository with no record is adoptable, and the binding that follows is
// not an inferred one: the working copy's own path hashes to this gate, which
// is the same evidence that creates a gate in the first place. A record that
// will not read is not adoptable, for the same reason a record naming somebody
// else is not, because the binding it carries is exactly the fact that cannot
// then be established.
//
// Every binding this establishes is reported as evidenced, including one over
// a record that says an earlier binding was inferred. The evidence is the same
// either way and it is evidence no copy can produce, because a copy stands at
// another path and hashes elsewhere. Carrying the earlier answer forward here
// would leave a gate whose owner is standing exactly where the gate is named
// for permanently unremovable, on the strength of a moment that has passed.
func ownGateBinding(ctx context.Context, set settings, repo, id, workingPath string) (adopted bool, err error) {
	rec, exists, err := readRecord(repo)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if rec.ID != id {
		return false, fmt.Errorf("%w: %s records identifier %q, but its name says %q",
			ErrMalformedRecord, repo, rec.ID, id)
	}
	if err := ensureAvailableTo(ctx, set, repo, rec, workingPath, fmt.Sprintf(
		"both paths hash to %s, so there is no second gate to hand out; the gate is handed over as soon as %s stops "+
			"pointing at it, so removing the %s remote there detaches it and always succeeds, and initializing %s "+
			"again then takes the gate over",
		id, rec.WorkingPath, RemoteName, workingPath)); err != nil {
		return false, err
	}
	return false, nil
}

// availableTo reports whether a gate carrying rec is a gate the working copy
// at workingPath may act on: either the record names it, or the record names a
// working copy that has since let go of the gate.
//
// It is the one place this package decides that question. Every caller takes
// its answer, and no caller reaches stillBound or compares a recorded path
// itself, so adoption and deletion cannot drift apart. What differs between
// callers is what they do with a false, which is why this reports rather than
// refuses.
func availableTo(ctx context.Context, set settings, repo string, rec record, workingPath string) (bool, error) {
	if rec.WorkingPath == workingPath {
		return true, nil
	}
	held, err := stillBound(ctx, set, rec.WorkingPath, repo)
	if err != nil {
		return false, err
	}
	// The working copy the gate records is gone, so there is nobody left to
	// take the gate from.
	return !held, nil
}

// ensureAvailableTo is availableTo for a caller that must not proceed without
// it. The remedy differs by operation and is the caller's to word; what a gate
// is and who holds it is not.
func ensureAvailableTo(ctx context.Context, set settings, repo string, rec record, workingPath, remedy string) error {
	available, err := availableTo(ctx, set, repo, rec, workingPath)
	if err != nil || available {
		return err
	}
	return fmt.Errorf("%w: the gate at %s belongs to the working copy at %s, which still points at it, "+
		"and %s is not that working copy; %s",
		ErrGateClaimed, repo, rec.WorkingPath, workingPath, remedy)
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

// contents is what a path filed where this home keeps its gates actually
// holds. It is three conditions rather than two, and keeping them apart is the
// point of the type.
//
// A gate whose record file is gone and a gate that is not there at all give
// the same answer to "can the record be read", and opposite answers to what
// the caller should do: the first is a gate to repair, holding everything its
// runs recorded, and the second is nothing, with nothing to keep and nothing
// to carry across a move. Collapsing them into one boolean is what let a
// deleted gate be reported as a reattachment.
type contents int

const (
	// noRepository is a path holding nothing: not there, or an empty
	// directory left behind by an initialization that died part way.
	noRepository contents = iota
	// repositoryWithoutRecord is a repository whose record is gone. It still
	// holds its references, so it is a gate to repair.
	repositoryWithoutRecord
	// repositoryWithRecord is a repository carrying a record this build reads.
	repositoryWithRecord
)

// gateRepository refuses with ErrNotAGate a path that is not filed where this
// home keeps its gates, and otherwise reports what that path holds, together
// with the record when there is one. It is what stands between Remove and
// deleting a directory only because a remote pointed at it.
//
// Deciding what a path is and reading who it belongs to are one read of one
// file, so a caller cannot be told a path is a gate and then read a different
// answer out of it a moment later.
func gateRepository(home, repo string) (rec record, holds contents, err error) {
	if filepath.Dir(repo) != repositoriesDir(home) || !strings.HasSuffix(repo, ".git") {
		return record{}, noRepository, fmt.Errorf("%w: %s is not in %s", ErrNotAGate, repo, repositoriesDir(home))
	}
	if holdsNoRepository(repo) {
		return record{}, noRepository, nil
	}
	rec, exists, err := readRecord(repo)
	if err != nil {
		return record{}, noRepository, err
	}
	if !exists {
		return record{}, repositoryWithoutRecord, nil
	}
	return rec, repositoryWithRecord, nil
}

// identifierAt is the identifier a gate repository is filed under in this
// home, or the empty string when the path is not one of this home's. It is
// where the identifier of a gate whose record is gone comes from, so the name
// is checked against repositoryPath rather than trusted to be one.
func identifierAt(home, repo string) string {
	id := strings.TrimSuffix(filepath.Base(repo), ".git")
	if id == "" || repositoryPath(home, id) != repo {
		return ""
	}
	return id
}

// ensureRepository creates the gate's bare repository when it is not there,
// leaves the one that is there alone, and in both cases refuses a hook that
// arrived with the repository rather than from this package.
//
// The refusal is scoped to the operation rather than to the case that first
// motivated it. What puts a hook into a gate from outside is a git template
// chosen by configuration this process cannot see past, which internal/vcs
// documents that it cannot close; see doc.go. That channel is open on every
// initialization and not only on the one that creates the repository, so the
// hooks directory is listed on both sides of the call and any name that was
// not there before and is there after is refused. A hook this package did not
// write would otherwise be moved to the .local name and chained into
// admission, which is an ancestor process choosing code that runs inside the
// gate.
//
// What the refusal undoes is what this call added: the repository, when this
// call is the one that created it, and otherwise the hooks that appeared. A
// hook left in place would be there before the next initialization, and a hook
// that was already there is a hook this refusal no longer sees.
func ensureRepository(ctx context.Context, repo string) error {
	parent := filepath.Dir(repo)
	// The assistant home holds every gated project's whole history, alongside
	// the lock, the socket, and the database PRD section 8 puts there, so the
	// directories this package creates under it are the owner's alone.
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("gate: creating %s: %w", parent, err)
	}
	creating := !isDirectory(repo) || isEmptyDirectory(repo)
	before, err := activeHooks(repo)
	if err != nil {
		return err
	}
	if _, err := vcs.InitBare(ctx, repo); err != nil {
		return fmt.Errorf("gate: creating the gate repository at %s: %w", repo, err)
	}
	after, err := activeHooks(repo)
	if err != nil {
		return err
	}
	arrived := namesAdded(before, after)
	if len(arrived) == 0 {
		return nil
	}
	if err := undoArrivedHooks(repo, creating, arrived); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s gained %s while being initialized, chosen by an init.templateDir this process cannot see past",
		ErrTemplateHooks, repo, strings.Join(arrived, ", "))
}

// undoArrivedHooks puts back what the initialization that is about to be
// refused had already done, so that the refusal is not a state the next
// initialization inherits and accepts.
func undoArrivedHooks(repo string, creating bool, arrived []string) error {
	if creating {
		if err := os.RemoveAll(repo); err != nil {
			return fmt.Errorf("gate: deleting the repository created at %s carrying template hooks: %w", repo, err)
		}
		return nil
	}
	for _, name := range arrived {
		path := filepath.Join(hooksDir(repo), name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("gate: removing %s, which arrived from a template during initialization: %w", path, err)
		}
	}
	return nil
}
