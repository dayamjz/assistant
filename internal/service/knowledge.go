package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/knowledge"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The repository's curated knowledge document, read from the trusted copy.
//
// internal/knowledge owns what the document is and how it is composed; this
// file owns where its bytes come from, which is the one decision that makes
// it curated. They are read out of the gate repository at a commit a fresh
// fetch of the default branch resolved, on the terms layers.go reads the
// trusted configuration copy, and never at a pushed commit or out of a
// working copy: P7 has nothing on a branch under validation direct what an
// agent working with this program's credentials is told, and a document a
// branch could rewrite for its own review is exactly that.
//
// # Who is told, and when
//
// A launched worker is told the document in its first turn: launchTask
// fetches the default branch for the launch, reads the document at the tip
// it resolved, records that revision on the attempt through
// store.SetAttemptKnowledge before the process starts, and adds the section
// after the task's instructions and the relaunch note and before the route
// section. Every launch reads afresh, a relaunch and a fallback switch
// included, because each is an attempt of its own and the record is per
// attempt.
//
// A run's review is told the document read at the trusted commit its
// configuration was resolved at (resolveRunConfig), which the resolution's
// digest names, so a run is told the document its configuration came from;
// a run under suppress_project_instructions is told none. internal/stages'
// review body places it, and the push stage's review of later commits shares
// that body. No other stage composes a prompt from project context: a fix
// round's fixer is told the findings it is asked to resolve and nothing of
// the document. That is a gap and not a rule; a fixer told the document
// would be told it on these same terms.
//
// # Refusals
//
// A document internal/knowledge refuses to compose, over its bound or not
// text, or whose section would make the composed first turn exceed a launch
// bound the resolved adapter owns, refuses the launch: knowledge-refused is recorded against the
// attempt, anchored to the history the dispatch was decided from, and the
// task resolves Failed from it rather than starting a worker told part of
// the document or nothing in its place. A run whose trusted document is
// refused does not start, on resolveRunConfig's terms. A default branch that
// cannot be fetched, or a document that cannot be read, is
// ErrTrustedKnowledge at a launch, which leaves the attempt standing for a
// retry on the terms any other launch read failure does, and
// ErrTrustedConfig at a run, which refuses it. A commit whose tree holds no
// document composes nothing and refuses nothing.
//
// # What is stored and what is not
//
// The attempt row names the commit and the digest of the document's bytes
// there, and nothing else. The text is not stored: it is the repository's,
// its history is the audit of who changed it and why, and the commit and
// digest name what the default branch held there. Reproducing what the worker
// was told takes internal/redact as well as git, because the section is the
// redacted text. The section a worker is sent passes internal/redact first,
// in internal/knowledge, so a credential that shape recognizes in the
// document reaches no worker and no reviewer.

// ErrTrustedKnowledge reports that the curated knowledge document could not
// be read from the default branch for a launch: the branch could not be
// fetched or resolved, or the document there could not be read. Nothing is
// launched without it, because launching on no document when one may stand
// would be a guess at what the worker should have been told.
var ErrTrustedKnowledge = errors.New("service: the project knowledge on the default branch could not be read, so nothing is launched without it")

// knowledgeRefusedKind is the kind internal/taskstate gives a launch refused
// over a document internal/knowledge would not compose.
const knowledgeRefusedKind = "knowledge-refused"

// knowledgeRef is the name the gate repository holds the default branch's
// tip under while a launch reads the knowledge document at it: named for the
// task and the attempt so no two reads overwrite each other's, and deleted
// once read.
func knowledgeRef(taskID string, attempt int64) string {
	return "refs/assistant/knowledge/" + taskID + "/" + strconv.FormatInt(attempt, 10)
}

// knowledgeAt reads the curated knowledge document at commit in the gate
// repository, for forWhat. A tree with no document is knowledge.Absent. A
// read that fails, and a document internal/knowledge refuses, are reported
// under class, the reader's own refusal, and the second still matches
// knowledge.ErrTooLarge or knowledge.ErrNotText beneath it.
func knowledgeAt(ctx context.Context, bare *vcs.Repository, commit, forWhat string, class error) (knowledge.Knowledge, error) {
	document, err := bare.FileAt(ctx, commit, knowledge.Document)
	if errors.Is(err, vcs.ErrPathNotFound) {
		return knowledge.Absent(commit), nil
	}
	if err != nil {
		return knowledge.Knowledge{}, fmt.Errorf("%w: reading %s at %s for %s: %w",
			class, knowledge.Document, commit, forWhat, err)
	}
	read, err := knowledge.Read(commit, document, redact.New())
	if err != nil {
		return knowledge.Knowledge{}, fmt.Errorf("%w: for %s: %w", class, forWhat, err)
	}
	return read, nil
}

// launchKnowledge reads the curated knowledge document a launch of task
// tells its worker: the default branch is fetched afresh into the gate
// repository and the document read at the tip it resolved. A document
// internal/knowledge refuses is recorded as knowledgeRefusedKind on the terms
// refuseKnowledge states and returned as the typed *knowledgeRefused; any
// other failure is ErrTrustedKnowledge and records nothing.
func (s *Service) launchKnowledge(ctx context.Context, task store.Task, d dispatched) (knowledge.Knowledge, error) {
	forWhat := "task " + task.ID
	repositoryID, ok := task.RepositoryID.Get()
	if !ok {
		return knowledge.Knowledge{}, fmt.Errorf("%w: %s names no repository to read it from", ErrTrustedKnowledge, forWhat)
	}
	repository, err := s.store.Repository(ctx, repositoryID)
	if err != nil {
		return knowledge.Knowledge{}, fmt.Errorf("%w: reading the repository of %s: %w", ErrTrustedKnowledge, forWhat, err)
	}
	bare, err := s.gateRepositoryOf(ctx, repository, forWhat)
	if err != nil {
		return knowledge.Knowledge{}, fmt.Errorf("%w: %w", ErrTrustedKnowledge, err)
	}
	ref := knowledgeRef(task.ID, d.attempt)
	defer func() { _ = bare.DeleteRef(context.WithoutCancel(ctx), ref) }()
	at, err := fetchTrusted(ctx, bare, repository, ref, forWhat, ErrTrustedKnowledge)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	read, err := knowledgeAt(ctx, bare, at, forWhat, ErrTrustedKnowledge)
	if errors.Is(err, knowledge.ErrTooLarge) || errors.Is(err, knowledge.ErrNotText) {
		return knowledge.Knowledge{}, s.refuseKnowledge(ctx, d, err)
	}
	return read, err
}

// knowledgeSection is the text a launch adds to a worker's first turn for
// k: nothing for a document that composes nothing, and otherwise the
// section, set apart from what precedes it the way the route section is.
func knowledgeSection(k knowledge.Knowledge) string {
	section := k.Section()
	if section == "" {
		return ""
	}
	return "\n\n" + strings.TrimRight(section, "\n")
}

// refuseKnowledgeIfLaunchTooLarge records knowledge-refused when firstTurn
// would be refused by the resolved adapter's launch bounds, which launch.go
// asks through WorkerLaunchRefusal after the route is chosen so the spec
// matches what Create would start.
func (s *Service) refuseKnowledgeIfLaunchTooLarge(ctx context.Context, d dispatched, runner agents.Runner,
	isolation tasks.Isolation, firstTurn string, project knowledge.Knowledge, model, effort string) error {
	if !project.Present() || knowledgeSection(project) == "" {
		return nil
	}
	workers, ok := runner.(agents.WorkerRunner)
	if !ok {
		return nil
	}
	spec := agents.WorkerSpec{
		Dir:          isolation.Copy,
		Instructions: firstTurn,
		Route:        s.workerRoute,
		Model:        model,
		Effort:       effort,
	}
	var term agents.Terminal
	if s.cfg.SessionProvider == config.SessionProviderTmux {
		tmux, err := exec.LookPath("tmux")
		if err != nil {
			return nil
		}
		tmux, err = filepath.Abs(tmux)
		if err != nil {
			return nil
		}
		term = agents.Terminal{Tmux: tmux, Socket: s.home.TerminalSocket("0000000000000000")}
	}
	if err := workers.WorkerLaunchRefusal(term, spec); err != nil {
		if !errors.Is(err, agents.ErrInvalidInvocation) {
			return fmt.Errorf("service: checking the launch of task %s attempt %d: %w", d.id, d.attempt, err)
		}
		return s.refuseKnowledge(ctx, d, fmt.Errorf("%w: %w", knowledge.ErrLaunchTooLarge, err))
	}
	return nil
}

// refuseKnowledge records the refusal as the task's knowledgeRefusedKind
// event, anchored to the history the dispatch was decided from, and returns
// the typed refusal. The task resolves Failed from it, so a document nobody
// can compose does not leave the attempt Dispatched forever.
func (s *Service) refuseKnowledge(ctx context.Context, d dispatched, cause error) error {
	s.log.Printf("task %s attempt %d was refused a launch over its project knowledge: %v", d.id, d.attempt, cause)
	event, err := s.store.AppendTaskEventAfter(ctx, d.id, d.anchor, knowledgeRefusedKind, cause.Error())
	if err != nil {
		return fmt.Errorf("service: recording the knowledge refusal of task %s: %w", d.id, err)
	}
	return &knowledgeRefused{Task: d.id, Attempt: d.attempt, Event: event, Cause: cause}
}

// knowledgeRefused reports a launch refused because the repository's curated
// knowledge document on its default branch is one internal/knowledge will
// not compose. The task resolves Failed from the recorded event.
type knowledgeRefused struct {
	Task    string
	Attempt int64
	Event   store.TaskEvent
	Cause   error
}

func (e *knowledgeRefused) Error() string {
	return fmt.Sprintf("service: task %s attempt %d: its project knowledge cannot be composed: %v",
		e.Task, e.Attempt, e.Cause)
}

func (e *knowledgeRefused) Unwrap() error { return e.Cause }
