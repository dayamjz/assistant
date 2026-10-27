package tasks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD principle P11: a worker does not launch unless the resolved path is a
// real isolated copy, distinct from the primary checkout, and being a linked
// copy is not proof on its own when the tool can be pointed at its own
// repository. This file is the assertion half of that: a pure comparison of
// what a directory was observed to be against what a task's copy must be,
// with a typed refusal naming the comparison that failed. The launch half
// is internal/session's Create, which refuses an Isolation this assertion
// did not produce before it asks for a worker; what this file states about
// that seam is on Isolation.
//
// # Why file identity
//
// Every path comparison here is by file identity, os.SameFile after
// filepath.EvalSymlinks, and never by string. A copy reached through a
// symbolic link into the primary checkout spells differently and is the same
// directory, and on a case-insensitive filesystem two spellings that differ
// only in letter case are one directory too. internal/gate's identifier is a
// hash of a resolved path and its doc names letter case as the gap that
// leaves; this assertion does not widen it, because it asks the filesystem
// which files are one file rather than which strings are one string. A path
// that cannot be resolved or stat'ed is a comparison that cannot be made, and
// a comparison that cannot be made refuses (P6) rather than passing on an
// absence.
//
// Identity has one blind spot, and the repository directory comparison asks
// the entry itself about it: a .git under the copy that is a symbolic link
// to another repository's directory is, by identity, that directory, so the
// comparison alone takes it for the copy's own while every write git makes
// through it lands in the other repository. So .git under the copy must
// also be a directory and not a link, read without following one. What
// that does not cover is a link inside the repository directory, a refs
// directory or a config file pointing into another repository, which the
// assertion does not walk, so a write under or to it lands there;
// internal/safety's kept-copy verification does walk it, for a copy a
// person answers.
//
// # Why the remote set is exact
//
// PRD section 5 gives a delivery copy one remote, assistant at the gate, and
// an investigation copy none. The assertion requires exactly that set and not
// merely that assistant is right: any other remote, and any second URL or
// push URL on the one remote, is a route to a shared branch that does not
// pass the gate's hooks, and "every push that reaches the gate goes through
// the hooks it installs" says nothing about a push that reaches somewhere
// else.
//
// # What the environment check establishes
//
// The observation is taken under internal/vcs's own environment, so it says
// what the copy is and cannot say what a worker started with a GIT_DIR would
// see. That is a property of the launch environment, and the assertion takes
// the final environment the launcher will use and refuses when
// vcs.StripRedirecting would change it. Deriving the check from that function
// keeps the list of what redirects git in one place (P14); the cost is that an
// entry naming no variable, which StripRedirecting drops too, is refused on
// the same terms.

// ErrNotIsolated is the class every *IsolationError belongs to: the directory
// observed is not the isolated copy the task must launch in, or the
// environment it would launch with points git elsewhere.
var ErrNotIsolated = errors.New("tasks: the path is not an isolated copy of the task's")

// ErrExpectation reports an Expectation the assertion cannot be made against:
// a path missing or relative, a shape outside store.TaskShape's set, or a
// branch that the shape forbids or requires. It is not a failed comparison,
// because nothing was compared.
var ErrExpectation = errors.New("tasks: the isolation expectation cannot be asserted as given")

// Comparison names one of the checks AssertIsolated makes, and is the closed
// set a refusal picks from.
type Comparison string

const (
	// ComparisonEnvironment is the launch environment carrying a variable
	// vcs.StripRedirecting removes.
	ComparisonEnvironment Comparison = "environment"
	// ComparisonToplevel is the working copy's root not being the expected
	// copy.
	ComparisonToplevel Comparison = "toplevel"
	// ComparisonCommonDir is the repository directory differing from the
	// common directory, which is what a linked worktree looks like.
	ComparisonCommonDir Comparison = "common-dir"
	// ComparisonGitDir is the repository directory not being .git under the
	// expected copy, or .git under it being a symbolic link rather than a
	// directory of the copy's own.
	ComparisonGitDir Comparison = "git-dir"
	// ComparisonPrimary is the copy being the primary checkout, inside it, or
	// containing it.
	ComparisonPrimary Comparison = "primary"
	// ComparisonGate is the copy being the gate repository or inside it.
	ComparisonGate Comparison = "gate"
	// ComparisonRemotes is the remote set differing from the one the shape
	// gives the copy.
	ComparisonRemotes Comparison = "remotes"
	// ComparisonHead is HEAD not being on the task's branch for a delivery
	// task, or not detached for an investigation task.
	ComparisonHead Comparison = "head"
)

// IsolationError reports a refused assertion and names the comparison that
// failed, so the record of the refusal says which fact was wrong rather than
// only that one was.
type IsolationError struct {
	// Comparison is the check that failed.
	Comparison Comparison
	// Detail says what was found, in words a person reading the refusal can
	// act on. Paths in it are as observed, unresolved.
	Detail string
}

// Error names the comparison and the detail.
func (e *IsolationError) Error() string {
	return fmt.Sprintf("tasks: not isolated (%s): %s", e.Comparison, e.Detail)
}

// Unwrap makes every refused assertion match ErrNotIsolated.
func (e *IsolationError) Unwrap() error { return ErrNotIsolated }

// Expectation is what a task's copy must be, from the task's record and the
// home it is under. Expect builds one from those, and the second check it
// builds beside it, so the two cannot disagree about the path.
type Expectation struct {
	// Copy is the path the copy must stand at: home.TaskCopy for the task.
	Copy string
	// Gate is this home's gate repository for the task's repository, which a
	// delivery copy's one remote must resolve to.
	Gate string
	// Primary is the person's own working copy, which the copy must be
	// distinct from: not it, not inside it, not containing it.
	Primary string
	// Shape decides the remote set and the HEAD rule.
	Shape store.TaskShape
	// Branch is the branch a delivery copy's HEAD must be on. It is required
	// for a delivery task and must be empty for an investigation task, whose
	// HEAD must be detached.
	Branch string
}

// Expect computes what the copy of task id must be, under h, and the command
// a worker is told to run in that copy as PRD principle P11's second check.
// Both come out of this one call from the same values, which is what keeps
// the path the worker is told to check and the path this program asserts
// from drifting apart.
//
// The command is detection and not prevention. It is a POSIX shell line
// asserting the three things an obedient worker can be asked to confirm
// before it creates a branch: that the working copy it is in is the copy,
// that the repository directory is .git under it, and that the remote set is
// the one its shape gives it. Each clause compares what git reports from the
// current directory with what git reports from the expected copy, so the
// form git prints a path in on the platform cancels out; the remote URL is
// compared as a string against the cleaned gate path in slash form, after
// the normalizations AssertIsolated applies to it, a leading file:// and a
// trailing separator removed and a separator before a volume with them, and
// with backslashes turned to slashes on both sides, so a spelling the
// assertion passes is one the line passes. What the line cannot do is
// compare by identity: a gate spelled through a symbolic link, or with
// letter case or a volume written another way, passes the assertion and
// fails the line. A worker that does not run it, or
// runs it and ignores a non-zero status, is not stopped by it; what stops a
// worker is the assertion this program makes before it launches, which
// internal/session's Create takes as the value it cannot start without.
func Expect(h *home.Home, id string, shape store.TaskShape, gatePath, primary, branch string) (Expectation, string, error) {
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		return Expectation{}, "", err
	}
	e := Expectation{Copy: copyPath, Gate: gatePath, Primary: primary, Shape: shape, Branch: branch}
	if err := e.validate(); err != nil {
		return Expectation{}, "", err
	}
	return e, secondCheck(e), nil
}

// secondCheck is the shell line Expect documents, built from the expectation
// and nothing else.
func secondCheck(e Expectation) string {
	q := shellQuote
	clauses := []string{
		`test "$(git rev-parse --show-toplevel)" = "$(git -C ` + q(e.Copy) + ` rev-parse --show-toplevel)"`,
		`test "$(git rev-parse --absolute-git-dir)" = "$(git rev-parse --show-toplevel)/.git"`,
	}
	if e.Shape == store.TaskDelivery {
		// The URL is read as urlAsPath reads it: a leading file:// comes off,
		// a separator before a volume with it, and one trailing separator
		// after; both sides are then put in slash form, since a gate path on
		// Windows is written with backslashes and the URL git hands back may
		// not be. A spelling the assertion accepts is then one this line
		// accepts, up to what a string comparison can see.
		clauses = append(clauses,
			`test "$(git remote)" = `+q(gate.RemoteName),
			`url="$(git remote get-url `+q(gate.RemoteName)+`)"`,
			`url="$(printf '%s' "$url" | tr '\\' '/')"`,
			`url="${url#file://}"`,
			`case "$url" in /[A-Za-z]:*) url="${url#/}";; esac`,
			`test "${url%/}" = `+q(filepath.ToSlash(filepath.Clean(e.Gate))),
			`test -z "$(git config --get-all remote.`+gate.RemoteName+`.pushurl)"`,
		)
	} else {
		clauses = append(clauses, `test -z "$(git remote)"`)
	}
	return strings.Join(clauses, " && ")
}

// shellQuote wraps s in single quotes for a POSIX shell, closing and
// reopening around any single quote in it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (e Expectation) validate() error {
	for _, p := range []struct{ name, value string }{{"copy", e.Copy}, {"gate", e.Gate}, {"primary", e.Primary}} {
		if p.value == "" {
			return fmt.Errorf("%w: the %s path is empty", ErrExpectation, p.name)
		}
		if !filepath.IsAbs(p.value) {
			return fmt.Errorf("%w: the %s path %q is not absolute", ErrExpectation, p.name, p.value)
		}
	}
	switch e.Shape {
	case store.TaskDelivery:
		if e.Branch == "" {
			return fmt.Errorf("%w: a delivery task names the branch its copy is on", ErrExpectation)
		}
	case store.TaskInvestigation:
		if e.Branch != "" {
			return fmt.Errorf("%w: an investigation task's copy is detached and names no branch", ErrExpectation)
		}
	default:
		return fmt.Errorf("%w: shape %q is not a task shape", ErrExpectation, e.Shape)
	}
	return nil
}

// Isolation is the proof that an observed directory is a task's isolated
// copy and that the environment it will be launched with points git at
// nothing else. Only AssertIsolated produces one that Proven reports true;
// the zero value is not a proof and reports false, so a seam that takes an
// Isolation can refuse one nobody asserted.
//
// internal/session's Create takes it as the value a worker cannot be
// started without, and refuses one Proven reports false. internal/service's
// dispatch produces one and holds it in memory, and its launch produces
// another, from a second observation at launch, which is the one Create is
// handed. That is the launch edge as far as this build takes it:
// internal/agents itself still starts a worker where it is told, so the
// edge holds for a launch through internal/session and nothing here closes
// a launch that bypasses it. The exported fields are what was proven, as
// resolved; a consumer compares against them rather than re-deriving.
type Isolation struct {
	// Copy is the copy's path with every symbolic link resolved, the
	// directory a launch taking this proof would use.
	Copy string
	// Shape is the shape the copy was proven for.
	Shape store.TaskShape
	// Branch is the branch a delivery copy was proven to be on, and empty
	// for an investigation copy.
	Branch string

	proven bool
}

// Proven reports whether this value came out of AssertIsolated. A zero
// Isolation did not.
func (i Isolation) Proven() bool { return i.proven }

// AssertIsolated compares an observation of a directory, taken by
// vcs.Observe, against what the task's copy must be, and returns the proof or
// a refusal. It is pure apart from resolving paths against the filesystem,
// which identity comparison needs: it starts no process and changes nothing.
//
// In order, it refuses when:
//
//   - env carries an entry vcs.StripRedirecting would remove
//     (ComparisonEnvironment);
//   - the working copy's root is not want.Copy (ComparisonToplevel);
//   - the repository directory is not the common directory, which is what a
//     linked worktree of any repository looks like (ComparisonCommonDir);
//   - the repository directory is not .git under want.Copy, or .git under
//     want.Copy is a symbolic link rather than a directory of the copy's
//     own (ComparisonGitDir);
//   - the copy is want.Primary, inside it, or contains it
//     (ComparisonPrimary);
//   - the copy is want.Gate or inside it (ComparisonGate);
//   - the remote set is not exactly one remote named gate.RemoteName with one
//     URL and no push URL, resolving to want.Gate, for a delivery task, or
//     exactly no remote for an investigation task (ComparisonRemotes);
//   - HEAD is not on want.Branch for a delivery task, or is not detached for
//     an investigation task (ComparisonHead).
//
// A remote's URL is compared as a path: a leading "file://" is removed and a
// trailing separator is cleaned away, because both spell the same gate
// repository, and the result is resolved and compared by identity. Any other
// form of URL is not a path this assertion can resolve to the gate and is
// refused, which is the right answer for a copy whose one remote names a
// repository on a host.
//
// Every refusal is an *IsolationError naming the comparison, and an
// expectation the assertion cannot be made against is ErrExpectation. A path
// that does not exist or cannot be resolved makes its comparison fail rather
// than pass.
func AssertIsolated(obs vcs.Observation, env []string, want Expectation) (Isolation, error) {
	if err := want.validate(); err != nil {
		return Isolation{}, err
	}
	if stripped := vcs.StripRedirecting(env); len(stripped) != len(env) {
		return Isolation{}, &IsolationError{Comparison: ComparisonEnvironment,
			Detail: fmt.Sprintf("the launch environment carries %d entries vcs.StripRedirecting removes", len(env)-len(stripped))}
	}

	copyPath, err := resolve(want.Copy)
	if err != nil {
		return Isolation{}, &IsolationError{Comparison: ComparisonToplevel,
			Detail: fmt.Sprintf("the expected copy %s cannot be resolved: %v", want.Copy, err)}
	}
	if same, err := sameFile(obs.Toplevel, want.Copy); err != nil || !same {
		return Isolation{}, &IsolationError{Comparison: ComparisonToplevel,
			Detail: fmt.Sprintf("the working copy's root is %s, want %s%s", obs.Toplevel, want.Copy, because(err))}
	}
	if same, err := sameFile(obs.GitDir, obs.CommonDir); err != nil || !same {
		return Isolation{}, &IsolationError{Comparison: ComparisonCommonDir,
			Detail: fmt.Sprintf("the repository directory %s is not the common directory %s, which is a linked worktree%s",
				obs.GitDir, obs.CommonDir, because(err))}
	}
	dotGit := filepath.Join(want.Copy, ".git")
	if same, err := sameFile(obs.GitDir, dotGit); err != nil || !same {
		return Isolation{}, &IsolationError{Comparison: ComparisonGitDir,
			Detail: fmt.Sprintf("the repository directory is %s, want .git under %s%s", obs.GitDir, want.Copy, because(err))}
	}
	// Identity followed a link at .git itself, so the entry is read without
	// following one: a link there reaches another repository's directory.
	switch entry, err := os.Lstat(dotGit); {
	case err != nil:
		return Isolation{}, &IsolationError{Comparison: ComparisonGitDir,
			Detail: fmt.Sprintf(".git under %s cannot be read: %v", want.Copy, err)}
	case entry.Mode()&fs.ModeSymlink != 0:
		return Isolation{}, &IsolationError{Comparison: ComparisonGitDir,
			Detail: fmt.Sprintf(".git under %s is a symbolic link to %s, not a repository directory of the copy's own", want.Copy, obs.GitDir)}
	}

	primary, err := resolve(want.Primary)
	if err != nil {
		return Isolation{}, &IsolationError{Comparison: ComparisonPrimary,
			Detail: fmt.Sprintf("the primary checkout %s cannot be resolved: %v", want.Primary, err)}
	}
	if rel, err := relation(copyPath, primary); err != nil || rel != distinct {
		return Isolation{}, &IsolationError{Comparison: ComparisonPrimary,
			Detail: fmt.Sprintf("the copy %s %s the primary checkout %s%s", want.Copy, rel.words(err), want.Primary, because(err))}
	}
	gatePath, err := resolve(want.Gate)
	if err != nil {
		return Isolation{}, &IsolationError{Comparison: ComparisonGate,
			Detail: fmt.Sprintf("the gate repository %s cannot be resolved: %v", want.Gate, err)}
	}
	if rel, err := relation(copyPath, gatePath); err != nil || rel == equal || rel == inside {
		return Isolation{}, &IsolationError{Comparison: ComparisonGate,
			Detail: fmt.Sprintf("the copy %s %s the gate repository %s%s", want.Copy, rel.words(err), want.Gate, because(err))}
	}

	if err := assertRemotes(obs, want, gatePath); err != nil {
		return Isolation{}, err
	}

	switch want.Shape {
	case store.TaskDelivery:
		if obs.Detached || obs.Branch != want.Branch {
			return Isolation{}, &IsolationError{Comparison: ComparisonHead,
				Detail: fmt.Sprintf("HEAD is %s, want the task's branch %s", headWords(obs), want.Branch)}
		}
	case store.TaskInvestigation:
		if !obs.Detached {
			return Isolation{}, &IsolationError{Comparison: ComparisonHead,
				Detail: fmt.Sprintf("HEAD is %s, want detached", headWords(obs))}
		}
	}
	return Isolation{Copy: copyPath, Shape: want.Shape, Branch: want.Branch, proven: true}, nil
}

// assertRemotes is the remote-set comparison, for both shapes.
func assertRemotes(obs vcs.Observation, want Expectation, gatePath string) error {
	if want.Shape == store.TaskInvestigation {
		if len(obs.Remotes) != 0 {
			return &IsolationError{Comparison: ComparisonRemotes,
				Detail: fmt.Sprintf("an investigation copy has no remote, and this one has %s", remoteNames(obs))}
		}
		return nil
	}
	if len(obs.Remotes) != 1 || obs.Remotes[0].Name != gate.RemoteName {
		return &IsolationError{Comparison: ComparisonRemotes,
			Detail: fmt.Sprintf("a delivery copy has exactly the remote %s, and this one has %s", gate.RemoteName, remoteNames(obs))}
	}
	r := obs.Remotes[0]
	if len(r.URLs) != 1 || len(r.PushURLs) != 0 {
		return &IsolationError{Comparison: ComparisonRemotes,
			Detail: fmt.Sprintf("the remote %s has %d URLs and %d push URLs, want one URL and none", r.Name, len(r.URLs), len(r.PushURLs))}
	}
	target, ok := urlAsPath(r.URLs[0])
	if !ok {
		return &IsolationError{Comparison: ComparisonRemotes,
			Detail: fmt.Sprintf("the remote %s names %q, which is not a path", r.Name, r.URLs[0])}
	}
	if same, err := sameFile(target, gatePath); err != nil || !same {
		return &IsolationError{Comparison: ComparisonRemotes,
			Detail: fmt.Sprintf("the remote %s is at %s, want this home's gate %s%s", r.Name, r.URLs[0], want.Gate, because(err))}
	}
	return nil
}

// urlAsPath reads a remote URL as a filesystem path. A plain path is itself,
// a "file://" prefix is removed, and a trailing separator is cleaned away;
// any URL with another scheme is not a path and ok is false. A relative
// path is one too, since it would be resolved against the copy rather than
// naming the gate.
func urlAsPath(url string) (path string, ok bool) {
	path = url
	if rest, found := strings.CutPrefix(url, "file://"); found {
		path = rest
		// file:///C:/gate spells a Windows path with a separator before the
		// volume; elsewhere the volume is empty and nothing is removed.
		if len(path) > 1 && path[0] == '/' && filepath.VolumeName(path[1:]) != "" {
			path = path[1:]
		}
	} else if i := strings.Index(url, "://"); i >= 0 {
		return "", false
	} else if strings.Contains(url, ":") && !filepath.IsAbs(url) {
		// user@host:path, which is an scp-style address and not a path.
		return "", false
	}
	if !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.Clean(path), true
}

// place is how one directory stands to another.
type place string

const (
	distinct place = "is distinct from"
	equal    place = "is"
	inside   place = "is inside"
	contains place = "contains"
)

// words renders the place, or that none could be decided when err is set.
func (p place) words(err error) string {
	if err != nil {
		return "cannot be placed against"
	}
	return string(p)
}

// relation says how the resolved directory a stands to the resolved directory
// b: the same, inside it, containing it, or neither. It walks each path's
// ancestors and asks the filesystem for identity at each, so a link anywhere
// on either path is seen through. An ancestor that cannot be stat'ed is an
// answer it cannot give.
func relation(a, b string) (place, error) {
	if same, err := sameFile(a, b); err != nil {
		return "", err
	} else if same {
		return equal, nil
	}
	within, err := isWithin(a, b)
	if err != nil {
		return "", err
	}
	if within {
		return inside, nil
	}
	within, err = isWithin(b, a)
	if err != nil {
		return "", err
	}
	if within {
		return contains, nil
	}
	return distinct, nil
}

// isWithin reports whether some proper ancestor of path is the directory
// root, by identity.
func isWithin(path, root string) (bool, error) {
	for dir := path; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
		same, err := sameFile(dir, root)
		if err != nil {
			return false, err
		}
		if same {
			return true, nil
		}
	}
}

// resolve makes path absolute, clean, and free of symbolic links, and fails
// when it does not exist.
func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// sameFile reports whether two paths name one file, by identity. Either path
// not existing is an error rather than a false, so a comparison against a
// missing path refuses.
func sameFile(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ia, ib), nil
}

// because renders an error that made a comparison undecidable, and nothing
// when the comparison was decided.
func because(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

func remoteNames(obs vcs.Observation) string {
	if len(obs.Remotes) == 0 {
		return "none"
	}
	names := make([]string, len(obs.Remotes))
	for i, r := range obs.Remotes {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

func headWords(obs vcs.Observation) string {
	if obs.Detached {
		return "detached"
	}
	return "on " + obs.Branch
}

// Refusal maps a refused assertion to the closed reason internal/taskstate
// gives an "isolation-refused" event, so the history carries a word that
// package defines rather than this one's prose. It is the one owner of that
// mapping, and ok is false for an error that is no *IsolationError.
//
// ComparisonPrimary is RefusedPrimaryCheckout. ComparisonRemotes and
// ComparisonHead are RefusedWrongShape: the copy is a repository of its own
// and distinct from the primary, and its remote set or its HEAD is not the
// one its shape gives it. Every other comparison is RefusedNotIsolated: the
// copy is not the one reserved, is a linked worktree, stands inside the gate
// repository, or would be launched under an environment that points git at
// another repository. RefusedCopyMissing is never answered here, because an
// assertion is made of a path something was observed at.
func Refusal(err error) (reason taskstate.IsolationRefusal, ok bool) {
	var refused *IsolationError
	if !errors.As(err, &refused) {
		return "", false
	}
	switch refused.Comparison {
	case ComparisonPrimary:
		return taskstate.RefusedPrimaryCheckout, true
	case ComparisonRemotes, ComparisonHead:
		return taskstate.RefusedWrongShape, true
	default:
		return taskstate.RefusedNotIsolated, true
	}
}
