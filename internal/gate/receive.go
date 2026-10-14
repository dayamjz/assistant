package gate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dayamjz/assistant/internal/vcs"
)

// ReceiveSubcommand is the third subcommand this package requires of the
// command whose path it writes, beside the two hooks.go names. It is not a
// hook: git runs it in place of git receive-pack when a working copy pushes to
// its assistant remote, because Initialize writes it as that remote's receive
// command. It must accept --gate and --home on the hooks' terms, followed by
// the path git appends, and hand all of it to Receive.
//
// It exists because a hook runs too late for the copies lock. git checks that a
// push is connected, which is the read a part-written worktree entry breaks,
// before it runs the admission hook, so a wait taken in that hook would begin
// after the push had already been refused. The receive command is the one point
// this package can reach that runs before git starts receiving.
const ReceiveSubcommand = "receive"

// ErrReceiveMismatch reports a receive command asked to receive into a
// repository other than the gate it was written for: the assistant remote's
// URL was changed after initialization and its receive command was not.
// Nothing is received. Initializing the working copy again writes both.
var ErrReceiveMismatch = errors.New("gate: the push is addressed to a repository this receive command was not written for")

// ReceiveSpec is one push arriving through a gate's receive command.
type ReceiveSpec struct {
	// Home and ID are what the command line Initialize wrote carries. They
	// name the gate whose copies lock is awaited and whose repository
	// receives, so the pushing environment chooses neither.
	Home string
	ID   string
	// Target is the path git appended to the command line, which is the
	// assistant remote's URL. It must name the same directory Home and ID
	// do.
	Target string
	// Stdin and Stdout are the push protocol, handed to git untouched;
	// nothing else is written to Stdout. Stderr is where the pushing git
	// shows the person what the receiving side said.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Wait bounds how long the push waits for the copies lock. A caller
	// passes ReceiveWait; a test passes something shorter.
	Wait time.Duration
	// Waiting is called once, before the push starts to wait, so the caller
	// can tell the person pushing why nothing is happening; the words are
	// the caller's. It may be nil.
	Waiting func()
}

// Receive serves one push into a gate once no run's copy is being cut or
// removed there, and refuses it with a *CopyInTransitError, before git is
// started, when one still is after spec.Wait.
//
// When it has to wait it calls spec.Waiting once, before the wait begins. It
// refuses with
// ErrReceiveMismatch when spec.Target is not the gate spec.Home and spec.ID
// name, and with RepositoryFor's refusals when they name no gate. Otherwise
// what git decides about the push is git's, through the hooks installed here,
// and a refusal git made comes back as the *vcs.CommandError for its exit.
//
// The lock is shared, so two pushes into one gate do not wait on each other,
// and it is held until git has finished, admission and notification included.
// A run a push starts therefore cuts its copy after the push that started it
// has returned, which costs that run nothing it would not have waited for.
func Receive(ctx context.Context, spec ReceiveSpec) error {
	repository, err := RepositoryFor(spec.Home, spec.ID)
	if err != nil {
		return err
	}
	if !sameDirectory(repository, spec.Target) {
		return fmt.Errorf("%w: it names %s and was written for gate %s at %s; initialize the working copy "+
			"again to point both at the gate", ErrReceiveMismatch, spec.Target, spec.ID, repository)
	}
	lock, err := AwaitCopies(ctx, repository, spec.Wait, spec.Waiting)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	bare, err := vcs.OpenBare(ctx, repository)
	if err != nil {
		return fmt.Errorf("gate: opening the gate repository %s to receive a push: %w", repository, err)
	}
	return bare.ServeReceivePack(ctx, spec.Stdin, spec.Stdout, spec.Stderr)
}

// sameDirectory reports whether two paths name one directory, by what the
// filesystem says rather than by how they are spelled.
func sameDirectory(a, b string) bool {
	if !filepath.IsAbs(b) {
		return false
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// receiveCommand renders the receive command Initialize writes for a
// working copy's assistant remote, on hookScript's terms: the command is an
// absolute path written with forward slashes, and the gate and the home are
// written beside it, so the pushing environment chooses none of the three.
// git appends the remote's path when it runs it.
func receiveCommand(home, id, command string) string {
	return fmt.Sprintf("%s gate %s --gate %s --home %s",
		shellQuote(filepath.ToSlash(command)), ReceiveSubcommand, shellQuote(id), shellQuote(home))
}
