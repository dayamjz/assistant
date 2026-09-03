package gate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/store"
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
// recorded against it, across the move.
//
// It reports false for a gate the current path does hash to, which includes a
// working copy that moved away and back again, and false on every later
// initialization of a gate whose record already names its working copy.
func (g *Gate) Reattached() bool { return g.reattached }

// Option configures an operation on a gate.
type Option func(*settings)

type settings struct {
	open  Opener
	index Index
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

// WithIndex supplies the home's record of which working copies are bound to
// which gate. It is required: every operation here has to know whether another
// working copy is still bound to the gate it is about to act on, and without an
// index that question is inferred rather than answered. There is no default,
// and an operation without one refuses with ErrNoIndex before it opens
// anything. See Index.
func WithIndex(index Index) Option {
	return func(s *settings) {
		if index != nil {
			s.index = index
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
// working copy that names a gate nobody else is bound to, and whose record does
// not already name it, is reattached to that gate, keeping its identifier and
// everything recorded against it. A working copy that names a gate another
// working copy is still bound to is a copy of that working copy, and gets its
// own gate rather than sharing the original's.
//
// It refuses with ErrGateClaimed when another working copy is still bound to
// the gate at the identifier this working copy's path hashes to, which is what
// a working copy placed at the path a moved one left behind meets. Taking that
// gate over would hand this working copy the other one's references and rewrite
// the binding everything recorded against the gate rests on.
//
// It refuses with ErrTemplateHooks when a hook arrives in the gate during the
// initialization rather than from this package, whether that initialization
// created the repository or repaired one that was already there, and with
// ErrCustomHookConflict when a hook it did not write cannot be preserved.
// Every refusal happens before the working copy's remote is written, so a
// refused initialization leaves the working copy as it was.
//
// A successful initialization records in the home's index that this working
// copy is bound to this gate, which is what makes the ownership question above
// answerable for every later operation, here and in any other working copy.
//
// Every gate this looks at is left refusing pushes if it has no admission
// hook, whatever this returns, so a refused initialization leaves a gate that
// admits nothing rather than one that admits everything. See doc.go.
func Initialize(ctx context.Context, spec Spec, opts ...Option) (*Gate, error) {
	var g *Gate
	err := withGate(ctx, spec, gateToRepair, opts, func(h *held) error {
		var err error
		g, err = h.initialize(ctx, spec.Command)
		return err
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// initialize is what an initialization does once it holds a gate. Whose gate it
// is has been settled, and this cannot ask again or forget to.
func (h *held) initialize(ctx context.Context, command string) (*Gate, error) {
	if err := validateCommand(command); err != nil {
		return nil, err
	}
	if err := ensureRepository(ctx, h.repository); err != nil {
		return nil, err
	}
	if err := installHooks(h.repository, h.id, command); err != nil {
		return nil, err
	}
	// The gate's own record and the home's binding are both written before the
	// working copy's configuration, so a failure up to this point leaves the
	// working copy exactly as it was and the gate repairable by running this
	// again. Neither is believed on its own afterwards, so a failure between
	// them leaves a state a later operation reads correctly rather than one it
	// has to reconcile.
	if err := writeRecord(h.repository, record{
		Version:     recordVersion,
		ID:          h.id,
		WorkingPath: h.workingPath,
	}); err != nil {
		return nil, err
	}
	if _, err := h.set.index.BindGate(ctx, h.workingPath, h.id); err != nil {
		return nil, fmt.Errorf("gate: recording that %s is bound to gate %s: %w", h.workingPath, h.id, err)
	}
	if err := h.copyOf.SetRemote(ctx, RemoteName, h.repository); err != nil {
		return nil, fmt.Errorf("gate: pointing the %s remote of %s at %s: %w",
			RemoteName, h.workingPath, h.repository, err)
	}
	return &Gate{id: h.id, repository: h.repository, workingPath: h.workingPath, reattached: h.reattached}, nil
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
//
// A gate carrying no record is repaired rather than deleted. Nothing inside it
// says whose it is, and this operation does not delete a repository on that
// footing; the message names the initialization that writes the record back,
// which succeeds because this operation has already established that no other
// working copy is bound to the gate.
//
// It refuses with ErrGateClaimed when another working copy is still bound to
// the gate the remote names, which is what a copy of a gated project meets: the
// copy inherited the original's configuration, so its remote names a gate that
// is not its own. Nothing is removed, not even the copy's remote, because a
// removal that acted on somebody else's gate on the strength of an inherited
// remote is the loss PRD principle P6 forbids and is the one this package
// cannot undo.
//
// A successful removal gives up the working copy's binding in the home's index
// as well as its remote, so a later operation is not told the deleted gate is
// still somebody's.
func Remove(ctx context.Context, spec Spec, opts ...Option) error {
	return withGate(ctx, spec, gateToRemove, opts, func(h *held) error {
		return h.remove(ctx)
	})
}

// remove is what a removal does once it holds a gate. Whose gate it is has been
// settled; what is left is the one condition removal alone refuses on, and the
// order the three things it gives up are given up in.
func (h *held) remove(ctx context.Context) error {
	detacher, ok := h.copyOf.(Detacher)
	if !ok {
		return fmt.Errorf("%w: %T cannot remove the %s remote of %s, so nothing was removed",
			ErrDetachUnsupported, h.copyOf, RemoteName, h.workingPath)
	}
	// A gate that is not there is refused before a handle exists, so what this
	// meets is a repository whose record is gone. It is written as everything
	// other than a record this build read, because the one condition a removal
	// may act on is the one it can name, not the one it can rule out.
	if h.holds != repositoryWithRecord {
		return recordlessRefusal(h.repository, h.workingPath)
	}
	// The irreversible step is last, so every step before it can be undone by
	// initializing again.
	if err := detacher.RemoveRemote(ctx, RemoteName); err != nil && !errors.Is(err, vcs.ErrRemoteNotFound) {
		return fmt.Errorf("gate: removing the %s remote of %s: %w", RemoteName, h.workingPath, err)
	}
	if err := h.set.index.UnbindGate(ctx, h.workingPath); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("gate: giving up the binding of %s: %w", h.workingPath, err)
	}
	if err := os.RemoveAll(h.repository); err != nil {
		return fmt.Errorf("gate: deleting %s: %w", h.repository, err)
	}
	return nil
}

// recordlessRefusal is what Remove answers when the gate a working copy names
// is there but carries no record. Nothing in it says whose gate it is, and
// removal will not delete a repository on that footing.
//
// The action it names is an initialization, and that action completes rather
// than leading to another refusal: this refusal is reached only after the
// resolution established that no other working copy is bound to the gate, so
// the initialization writes the record back and the removal after it succeeds.
func recordlessRefusal(repo, workingPath string) error {
	return fmt.Errorf("%w: %s carries no %s, so nothing there says whose gate it is and nothing was removed; "+
		"no other working copy is bound to it, so initializing %s writes the record back, and a removal after "+
		"that succeeds",
		ErrNotAGate, repo, recordName, workingPath)
}

// validatePaths turns a Spec into the two resolved paths every operation needs.
// The hook command is validated separately by validateCommand, because an
// initialization has a gate to seal before it may refuse over the command.
func validatePaths(spec Spec) (home, workingPath string, err error) {
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

// boundRepository returns the path a working copy's assistant remote names,
// cleaned, or the empty string when it has no such remote.
//
// What it is for is finding a gate, and that is all it is for. A remote is not
// evidence that a gate belongs to the working copy naming it: this package
// wrote that remote, and a copy of a gated project inherits it exactly. Who a
// gate belongs to is asked of the ownership index and the gate's own record;
// see held.claimants.
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
//
// It also seals, and this is the one place other than the seam that does. A
// repository takes pushes from the moment it exists, and this call is the one
// place a gate repository comes into existence, so the window between the git
// invocation above and the hooks being installed is a window only this function
// can close. Every gate that already existed when the operation started was
// sealed by the seam before the operation began, which is why that is the seam's
// half and this is this function's; the two do not overlap and there is no third.
//
// This half is the one the tests do not watch. The window it closes is left only
// by a failure between here and installHooks, which means an I/O error or the
// process dying, and neither is reachable through this package's exported
// surface. Removing this seal fails no test today. It is kept because the
// alternative is a repository that exists, accepts every push, and runs nothing
// until somebody happens to run another operation on that home, and it is
// written down here as reasoned rather than counted among the guards that have
// been watched to fail.
func ensureRepository(ctx context.Context, repo string) error {
	parent := filepath.Dir(repo)
	// The assistant home holds every gated project's whole history, alongside
	// the lock, the socket, and the database PRD section 8 puts there, so the
	// directories this package creates under it are the owner's alone.
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("gate: creating %s: %w", parent, err)
	}
	creating := holdsNoRepository(repo)
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
	if len(arrived) > 0 {
		if err := undoArrivedHooks(repo, creating, arrived); err != nil {
			return err
		}
	}
	// A repository that is still here takes pushes from this moment on, and
	// this is the only function that can leave behind one the seam has not
	// already seen. A repository this call created and then deleted again takes
	// nothing, and is left deleted rather than brought back by a seal.
	if !holdsNoRepository(repo) {
		if err := sealAdmission(repo); err != nil {
			return err
		}
	}
	if len(arrived) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s gained %s while being initialized; this package writes no hook before the "+
		"repository exists, and an init.templateDir in a configuration file it cannot see past is what puts one "+
		"there, so the hook was refused rather than adopted. The gate refuses every push until an initialization "+
		"succeeds, so take the hook out of that template, or point init.templateDir elsewhere, and initialize again",
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
