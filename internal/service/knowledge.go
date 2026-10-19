package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dayamjz/assistant/internal/knowledge"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// RecordOperatorKnowledge appends an operator knowledge row for project.
// The author is store.AuthoredByOperator because this method is the operator
// surface: it is not read from the caller, and it is not store.ResolvedByPerson.
// No surface here witnesses a person, so the row understates who wrote rather
// than claiming a person did.
//
// adopts, when not zero, names a task assertion of the same project. That
// assertion is not a rule until this row says so. PRD section 9 names no verb
// for this method, and internal/cli does not serve one. A test calls it.
func (s *Service) RecordOperatorKnowledge(ctx context.Context, project, body string, adopts int64) (store.Knowledge, error) {
	row, err := s.store.AppendKnowledge(ctx, store.KnowledgeInput{
		Project: project,
		Author:  store.AuthoredByOperator(),
		Body:    body,
		Adopts:  adopts,
	})
	if err != nil {
		return store.Knowledge{}, err
	}
	return row, nil
}

// RecordTaskAssertion appends a task's assertion. The author is
// store.AuthoredByTask. The row cannot adopt, and internal/knowledge does
// not inject it as a rule unless a later operator row names it. PRD section
// 9 names no verb for this method either.
func (s *Service) RecordTaskAssertion(ctx context.Context, project, taskID, body string) (store.Knowledge, error) {
	row, err := s.store.AppendKnowledge(ctx, store.KnowledgeInput{
		Project:    project,
		Author:     store.AuthoredByTask(),
		SourceTask: taskID,
		Body:       body,
	})
	if err != nil {
		return store.Knowledge{}, err
	}
	return row, nil
}

// ProjectKnowledge returns the project's knowledge history, oldest first.
func (s *Service) ProjectKnowledge(ctx context.Context, project string) ([]store.Knowledge, error) {
	return s.store.ProjectKnowledge(ctx, project)
}

// WorkerPrompt is the block launch appends for a project that has knowledge.
// repositoryDir is the checkout whose assistant.json must not replace the
// operator document; an empty directory reads no file. A project with no
// rows returns an empty block, which launch does not append.
//
// The trusted prefix is the operator configuration file this service's home
// holds. A missing file is an absent document, and the repository file is
// not substituted for it. Open holds that name a task of the project, and
// tasks of the project that have not resolved Complete, are passed as pins.
// An open hold that names no task is not pinned: the scope is the project,
// and a run hold does not name one.
func (s *Service) WorkerPrompt(ctx context.Context, project, repositoryDir string) (knowledge.Context, error) {
	rows, err := s.store.ProjectKnowledge(ctx, project)
	if err != nil {
		return knowledge.Context{}, err
	}
	if len(rows) == 0 {
		return knowledge.Context{}, nil
	}
	trusted, err := os.ReadFile(s.home.ConfigFile())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return knowledge.Context{}, fmt.Errorf("service: reading the operator document for %s: %w", project, err)
	}
	if errors.Is(err, os.ErrNotExist) {
		trusted = nil
	}
	var repo []byte
	if repositoryDir != "" {
		repo, err = os.ReadFile(filepath.Join(repositoryDir, "assistant.json"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return knowledge.Context{}, fmt.Errorf("service: reading assistant.json in %s: %w", repositoryDir, err)
		}
		if errors.Is(err, os.ErrNotExist) {
			repo = nil
		}
	}
	pins, err := s.knowledgePins(ctx, project)
	if err != nil {
		return knowledge.Context{}, err
	}
	out, err := knowledge.Compose(knowledge.Material{
		Trusted:        string(trusted),
		RepositoryFile: string(repo),
		Knowledge:      rows,
		Pinned:         pins,
		Budget:         knowledge.DefaultBudget,
	})
	if err != nil {
		return knowledge.Context{}, fmt.Errorf("service: composing knowledge for %s: %w", project, err)
	}
	return out, nil
}

func (s *Service) knowledgeForLaunch(ctx context.Context, taskID, copyPath string) (string, error) {
	task, err := s.store.Task(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("service: reading task %s for its knowledge: %w", taskID, err)
	}
	block, err := s.WorkerPrompt(ctx, task.Project, copyPath)
	if err != nil {
		return "", err
	}
	if block.Text == "" {
		return "", nil
	}
	if strings.HasPrefix(block.Text, "\n") {
		return block.Text, nil
	}
	return "\n" + block.Text, nil
}

func (s *Service) knowledgePins(ctx context.Context, project string) ([]knowledge.Pin, error) {
	resolved, err := s.tasks().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: reading tasks for %s: %w", project, err)
	}
	ofProject := map[string]taskstate.Resolved{}
	for _, row := range resolved {
		if row.Record.Project == project {
			ofProject[row.Record.ID] = row
		}
	}
	holds, err := s.store.OpenHolds(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: reading holds for %s: %w", project, err)
	}
	var pins []knowledge.Pin
	for _, hold := range holds {
		id, ok := hold.TaskID.Get()
		if !ok {
			continue
		}
		if _, ours := ofProject[id]; !ours {
			continue
		}
		pins = append(pins, knowledge.Pin{Kind: knowledge.PinHold, ID: hold.Key, Text: hold.Subject})
	}
	for id, row := range ofProject {
		if row.State.State == taskstate.Complete {
			continue
		}
		pins = append(pins, knowledge.Pin{Kind: knowledge.PinTask, ID: id, Text: string(row.State.State)})
	}
	slices.SortFunc(pins, func(a, b knowledge.Pin) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return pins, nil
}
