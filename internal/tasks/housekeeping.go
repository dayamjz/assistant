package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The intake of a housekeeping pass: PRD section 6's housekeeping, "who
// creates it", "instructions" and "shape", with section 9's housekeeping
// mark.
//
// A marked create records an investigation of the named repository under
// config.RoleHousekeeping, whose instructions are the program's part, written
// here and not changeable by the coordinator, followed by the coordinator's
// notes. It is refused, with nothing recorded, when it names a role
// (ErrRequest, as every role a task may not name is), a shape other than an
// investigation, a branch or a posture, or no repository; when the
// repository's trusted housekeeping.paths is empty (*PassRefusal); when that
// list cannot be read (ErrPassScopeUnreadable, never an assumed scope); and,
// under the store's serialization, while a pass of the repository is open
// (*store.PassOpenError naming it). Whether the coordinator was asked by a
// person or answered a wake is nothing this package can tell, so the
// pass is held to the same scope either way.
//
// The program's part names the scope, the base of the newest earlier pass
// and when it was created, the holds earlier passes raised that are still
// open, and the rules of section 6's table. It is composed from records read
// before the creating transaction, so a pass created between that read and
// the write is the refusal's to catch, never the brief's to describe.

// HousekeepingScope answers a repository's trusted housekeeping.paths, read
// from the default branch as internal/service reads every trusted key: a
// pushed copy has no say in it. task is the pass being created, which the
// producer may name what it fetches after. An error is a trusted copy that
// could not be read or parsed, and intake refuses on it rather than assuming
// the default list.
type HousekeepingScope func(ctx context.Context, repository store.Repository, task string) (config.PatternSet, error)

// ErrPassScopeUnreadable is returned, wrapped, by a marked create whose
// repository's trusted housekeeping.paths could not be read.
var ErrPassScopeUnreadable = errors.New("tasks: the repository's housekeeping scope could not be read from its trusted copy")

// ErrPassRefused is the class every *PassRefusal belongs to.
var ErrPassRefused = errors.New("tasks: the housekeeping pass is refused")

// PassRefusal is a marked create refused for what the repository's trusted
// configuration says, as opposed to a malformed request.
type PassRefusal struct {
	// Repository is the repository the pass was asked for.
	Repository string
	// Reason says why.
	Reason string
}

// Error names the repository and why.
func (e *PassRefusal) Error() string {
	return fmt.Sprintf("tasks: no housekeeping pass of repository %s is created: %s", e.Repository, e.Reason)
}

// Unwrap makes every *PassRefusal match ErrPassRefused.
func (e *PassRefusal) Unwrap() error { return ErrPassRefused }

// passScopeOff is the reason a repository whose trusted list is empty is
// refused.
const passScopeOff = "its trusted housekeeping.paths is empty, which turns housekeeping off for it"

// validPass refuses a marked request that is not a housekeeping pass's to
// make, before anything is read.
func validPass(req Request) error {
	switch {
	case strings.TrimSpace(req.Role) != "":
		return fmt.Errorf("%w: a housekeeping pass names no role; it runs under the %s role the program assigns, and %q was asked for",
			ErrRequest, config.RoleHousekeeping, req.Role)
	case strings.TrimSpace(req.Shape) != "" && store.TaskShape(strings.TrimSpace(req.Shape)) != store.TaskInvestigation:
		return fmt.Errorf("%w: a housekeeping pass is created as an investigation, and %q was asked for", ErrRequest, req.Shape)
	case strings.TrimSpace(req.Repository) == "":
		return fmt.Errorf("%w: a housekeeping pass names the repository whose agent context it keeps", ErrRequest)
	case strings.TrimSpace(req.Branch) != "":
		return fmt.Errorf("%w: a housekeeping pass is an investigation and creates no branch, and %q was asked for", ErrRequest, req.Branch)
	case strings.TrimSpace(req.Delivery) != "":
		return fmt.Errorf("%w: a housekeeping pass is an investigation and has no delivery posture, and %q was asked for", ErrRequest, req.Delivery)
	case strings.TrimSpace(req.Overlaps) != "":
		return fmt.Errorf("%w: a housekeeping pass is one at a time per repository and claims no overlap, and %q was asked for", ErrRequest, req.Overlaps)
	}
	return nil
}

// passFacts are the records the program's part of a pass's instructions is
// written from.
type passFacts struct {
	repository string
	scope      config.PatternSet
	// last is the newest earlier pass, and unknown before the first.
	last store.Optional[earlierPass]
	// holds are the open holds earlier passes raised.
	holds []store.Hold
}

// earlierPass is what the brief says of the newest earlier pass.
type earlierPass struct {
	id      string
	created time.Time
	// base is PassBase's, and unknown for a pass never dispatched.
	base store.Optional[string]
}

// PassBase is the base of a housekeeping pass, PRD section 6's: the
// default-branch commit its copy was cut at, which is what its worker read,
// as the copy's start snapshot records it at the pass's latest dispatch,
// and unknown for a pass never dispatched. It is not the knowledge revision
// the launch recorded: that is read at a fresh fetch of the upstream, which
// can stand past the primary checkout the copy was cut from, and a pass
// that found nothing at the older tree has not checked the newer commit.
// It is the one reading of the base, which the next pass's instructions
// take from here.
func PassBase(task store.Task) store.Optional[string] {
	snap, ok := task.StartSnapshot.Get()
	if !ok || snap.Head == "" {
		return store.Unknown[string]()
	}
	return store.Known(snap.Head)
}

// readPassFacts reads the repository's scope and its earlier passes.
func (q *Queue) readPassFacts(ctx context.Context, repository store.Repository, id string) (passFacts, error) {
	if q.scope == nil {
		return passFacts{}, fmt.Errorf("%w: this queue was built with no housekeeping scope reader", ErrPassScopeUnreadable)
	}
	scope, err := q.scope(ctx, repository, id)
	if err != nil {
		return passFacts{}, fmt.Errorf("%w: repository %s: %w", ErrPassScopeUnreadable, repository.ID, err)
	}
	if len(scope) == 0 {
		return passFacts{}, &PassRefusal{Repository: repository.ID, Reason: passScopeOff}
	}
	passes, err := q.store.HousekeepingPasses(ctx, repository.ID)
	if err != nil {
		return passFacts{}, err
	}
	facts := passFacts{repository: repository.ID, scope: scope}
	for i, p := range passes {
		if i == 0 {
			facts.last = store.Known(earlierPass{id: p.Task.ID, created: p.Task.CreatedAt, base: PassBase(p.Task)})
		}
		for _, h := range p.Holds {
			if h.Open() && h.TaskID.Or("") == p.Task.ID {
				facts.holds = append(facts.holds, h)
			}
		}
	}
	return facts, nil
}

// passDisposition is the disposition a pass is created with: independent,
// since a pass claims no overlap and names no branch, and decided against
// the one-open-pass rule by internal/taskstate's record tier.
func passDisposition() store.Disposition {
	return store.Disposition{Delivered: taskstate.Delivered}
}

// passInstructions is a pass's instructions: the program's part, then the
// coordinator's notes under a heading of their own, or a line saying there
// are none.
func passInstructions(f passFacts, notes string) string {
	var b strings.Builder
	b.WriteString("# Housekeeping pass of repository " + f.repository + "\n\n")
	b.WriteString("This is a housekeeping pass: a recurring task that keeps the repository's agent context true. The program " +
		"wrote this first part and the coordinator cannot change it. The coordinator's notes follow it.\n\n")

	b.WriteString("## Scope\n\n")
	b.WriteString("The agent context is the files these path patterns name, read from the default branch's trusted configuration " +
		"(housekeeping.paths). They are the only files this pass may change:\n\n")
	for _, p := range f.scope.Strings() {
		b.WriteString("- `" + p + "`\n")
	}
	b.WriteString("\nA pattern with no slash names that file at any depth.\n\n")

	b.WriteString("## Since the last pass\n\n")
	if last, ok := f.last.Get(); ok {
		fmt.Fprintf(&b, "The last pass was task %s, created at %s.", last.id, last.created.UTC().Format(time.RFC3339))
		if base, ok := last.base.Get(); ok {
			fmt.Fprintf(&b, " Its base, the default-branch commit its copy was cut at, is %s. Read what merged since then from "+
				"the repository, starting with git log %s..HEAD on the default branch, rather than from anyone's memory.\n\n", base, base)
		} else {
			b.WriteString(" It recorded no base, because it was never dispatched, so read the scoped files whole against the tree.\n\n")
		}
	} else {
		b.WriteString("This is the repository's first pass. There is no earlier base, so read the scoped files whole against the tree.\n\n")
	}

	b.WriteString("## Questions earlier passes already raised\n\n")
	if len(f.holds) == 0 {
		b.WriteString("None is open.\n\n")
	} else {
		b.WriteString("These decisions are still open. Do not raise them again; a finding that would repeat one is left out.\n\n")
		for _, h := range f.holds {
			fmt.Fprintf(&b, "- %s: %s\n", h.Key, h.Subject)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Rules\n\n")
	for _, rule := range passRules {
		b.WriteString("- " + rule + "\n")
	}

	b.WriteString("\n## The coordinator's notes\n\n")
	if text := strings.TrimSpace(notes); text != "" {
		b.WriteString("These are claims to check, not facts. Apply a note only where the repository bears it out, and say in your " +
			"report why you did not apply the rest.\n\n")
		b.WriteString(text + "\n")
	} else {
		b.WriteString("The coordinator added none.\n")
	}
	return b.String()
}

// passRules is section 6's table as the worker reads it.
var passRules = []string{
	"You start as an investigation. Report each claim in a scoped file that the repository at the default branch shows to be " +
		"false, with the file or command that proves it, as a fix finding. If you find none, say so in the summary and report no finding.",
	"Fix only factual drift: a claim in a scoped file that the code or the configuration owns and the file only describes, " +
		"such as a renamed command, a moved path or a removed key. Each correction names the file or command that proves it.",
	"Never add, remove or reword a rule, a principle or a specification. Where a file owns its fact, a disagreement between " +
		"it and the code is a finding about one of the two, not drift in the file. Report each such question as an ask finding " +
		"carrying the proposed wording and the evidence. The same goes for a correction a coordinator note asks for that reads " +
		"as a new rule, even when the note says the person decided it.",
	"Change nothing outside the scope above. Once the coordinator promotes this pass, a change that touches a path outside it " +
		"holds at review for a person.",
	"Edit only your own copy, and reach a shared branch only through the gate after promotion. Remove no copy, branch or file " +
		"outside the scope, and force nothing.",
	"Cleaning the machine is not this pass's job: copies and temporary entries are the program's to remove. Nothing outside " +
		"the project, such as a person's own configuration files, is in scope.",
}
