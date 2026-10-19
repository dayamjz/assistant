package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/home"
)

// MaxKnowledgeBody is the most bytes a knowledge body may carry, counted on
// the text the caller passed and again on the redacted text that would be
// stored. AppendKnowledge refuses anything longer and writes nothing.
const MaxKnowledgeBody = 4096

// MaxKnowledgeProject is the most bytes a knowledge project scope may carry.
const MaxKnowledgeProject = 1024

// KnowledgeAuthor says who wrote a knowledge row. It is a closed set on
// Sender's terms: a struct with one unexported field and no conversion into
// it, so outside this package the only values are the two the functions
// below return and the zero, and nothing decoded from a wire, a file, or an
// agent's output is one.
//
// There is no value meaning a person wrote it. No surface in this repository
// witnesses a person, and store.ResolvedByPerson is not a member of this set.
// AuthoredByOperator records that the operator's service method wrote the
// row. That method does not witness a person, so the record understates who
// wrote rather than claiming a person did.
//
// AppendKnowledge reads the author to decide which fields the row may carry:
// a task row names a source task and adopts nothing, and an operator row
// may adopt and names no source task. It refuses the zero author. It does
// not decide whether the row may be injected into a worker prompt; that is
// internal/knowledge's decision.
type KnowledgeAuthor struct{ name string }

// AuthoredByTask is a row a task asserted. Its source task is the identifier
// the caller named. AppendKnowledge checks that identifier with
// home.ValidTaskID and does not require a task row, so the record does not
// prove the task exists.
func AuthoredByTask() KnowledgeAuthor { return KnowledgeAuthor{name: "task"} }

// AuthoredByOperator is a row the operator's service method recorded.
// internal/service's RecordOperatorKnowledge is that method. PRD section 9
// names no verb for it, and this package does not invent one.
func AuthoredByOperator() KnowledgeAuthor { return KnowledgeAuthor{name: "operator"} }

// String renders who wrote, or "nobody" for the zero KnowledgeAuthor.
func (a KnowledgeAuthor) String() string {
	if a.name == "" {
		return "nobody"
	}
	return a.name
}

func knowledgeAuthors() []KnowledgeAuthor {
	return []KnowledgeAuthor{AuthoredByTask(), AuthoredByOperator()}
}

func knowledgeAuthorText(a KnowledgeAuthor) (string, error) {
	if a == (KnowledgeAuthor{}) {
		return "", ErrNoKnowledgeAuthor
	}
	for _, known := range knowledgeAuthors() {
		if a == known {
			return a.name, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownKnowledgeAuthor, a.name)
}

func knowledgeAuthorValue(text string) (KnowledgeAuthor, error) {
	for _, known := range knowledgeAuthors() {
		if text == known.name {
			return known, nil
		}
	}
	return KnowledgeAuthor{}, fmt.Errorf("store: column author: %w: %q", ErrUnknownKnowledgeAuthor, text)
}

// Knowledge is one appended knowledge row. It is not current state: a later
// row does not revise an earlier one, and the newest row is not what is true
// now.
type Knowledge struct {
	// ID is the row's identifier, which the store assigns. An operator row's
	// Adopts names one.
	ID int64 `json:"id"`
	// Project is the scope. Two projects do not share rows.
	Project string `json:"project"`
	// Author is who wrote it, from the closed set KnowledgeAuthor names. It
	// has no encoded form, on Sender's terms, so it does not travel.
	Author KnowledgeAuthor `json:"-"`
	// SourceTask is the task an AuthoredByTask row names, and empty for an
	// operator row.
	SourceTask string `json:"source_task,omitempty"`
	// Body is the text, with credentials removed by the redactor the store
	// was opened with, before the row was written.
	Body string `json:"body"`
	// Adopts names the task assertion an operator row adopts, and is unknown
	// when the row adopts nothing. A task assertion does not adopt.
	Adopts Optional[int64] `json:"adopts,omitempty"`
	// RecordedAt is when the row was appended.
	RecordedAt time.Time `json:"recorded_at"`
}

// KnowledgeInput is what AppendKnowledge records. Author is a KnowledgeAuthor
// the caller already holds; a string does not convert into one.
type KnowledgeInput struct {
	// Project is the scope.
	Project string
	// Author is who wrote it.
	Author KnowledgeAuthor
	// SourceTask is required for AuthoredByTask and must be empty otherwise.
	SourceTask string
	// Body is the text, redacted before it is stored.
	Body string
	// Adopts names a task assertion of the same project. Zero means the row
	// adopts nothing. Only AuthoredByOperator may set it.
	Adopts int64
}

// AppendKnowledge appends one knowledge row. It redacts the body through the
// store's redactor before the size bound and before the insert. A second
// call appends another row; nothing in this package updates or deletes one.
//
// A task author requires a source task identifier home.ValidTaskID accepts,
// and it may not adopt. An operator author may not name a source task. An
// adoption must name an existing task-authored row of the same project.
// The zero author, an empty body, an empty project, and a body or project
// over the size bound are refused, and nothing is written.
func (s *Store) AppendKnowledge(ctx context.Context, in KnowledgeInput) (Knowledge, error) {
	project := strings.TrimSpace(in.Project)
	if project == "" {
		return Knowledge{}, errors.New("store: knowledge names no project")
	}
	if len(project) > MaxKnowledgeProject || len(in.Project) > MaxKnowledgeProject {
		return Knowledge{}, fmt.Errorf("%w: project", ErrKnowledgeBound)
	}
	if strings.TrimSpace(in.Body) == "" {
		return Knowledge{}, ErrKnowledgeEmpty
	}
	if len(in.Body) > MaxKnowledgeBody {
		return Knowledge{}, fmt.Errorf("%w: body", ErrKnowledgeBound)
	}
	who, err := knowledgeAuthorText(in.Author)
	if err != nil {
		return Knowledge{}, fmt.Errorf("store: recording knowledge: %w", err)
	}
	switch in.Author {
	case AuthoredByTask():
		if err := home.ValidTaskID(in.SourceTask); err != nil {
			return Knowledge{}, fmt.Errorf("store: knowledge from a task: %w", err)
		}
		if in.Adopts != 0 {
			return Knowledge{}, fmt.Errorf("%w: a task assertion does not adopt", ErrKnowledgeAdopts)
		}
	case AuthoredByOperator():
		if in.SourceTask != "" {
			return Knowledge{}, fmt.Errorf("store: an operator record names no source task")
		}
		if in.Adopts < 0 {
			return Knowledge{}, fmt.Errorf("%w: %d", ErrKnowledgeAdopts, in.Adopts)
		}
	default:
		return Knowledge{}, fmt.Errorf("store: recording knowledge: %w", ErrUnknownKnowledgeAuthor)
	}
	if s.redact == nil {
		return Knowledge{}, ErrReadOnly
	}
	redacted := s.redact.Redact(in.Body)
	if len(redacted) > MaxKnowledgeBody {
		return Knowledge{}, fmt.Errorf("%w: body", ErrKnowledgeBound)
	}
	var out Knowledge
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var adopts any
		if in.Adopts != 0 {
			target, err := knowledgeByID(ctx, tx, in.Adopts)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrKnowledgeAdopts, err)
			}
			if target.Author != AuthoredByTask() || target.Project != project {
				return fmt.Errorf("%w: %d", ErrKnowledgeAdopts, in.Adopts)
			}
			adopts = in.Adopts
		}
		var source any
		if in.SourceTask != "" {
			source = in.SourceTask
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO knowledge (project, author, source_task, body, adopts, recorded_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			project, who, source, redacted, adopts, encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = knowledgeByID(ctx, tx, id)
		return err
	})
	if err != nil {
		return Knowledge{}, fmt.Errorf("store: recording knowledge for %s: %w", project, err)
	}
	return out, nil
}

// ProjectKnowledge returns every knowledge row scoped to project, oldest
// first. It is the whole history. The newest row is not a summary of it.
func (s *Store) ProjectKnowledge(ctx context.Context, project string) ([]Knowledge, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(project) == "" {
		return nil, errors.New("store: knowledge names no project")
	}
	rows, err := s.read.QueryContext(ctx,
		knowledgeColumns+` FROM knowledge WHERE project = ? ORDER BY id`, project)
	if err != nil {
		return nil, fmt.Errorf("store: reading knowledge for %s: %w", project, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Knowledge
	for rows.Next() {
		row, err := scanKnowledge(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading knowledge for %s: %w", project, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading knowledge for %s: %w", project, err)
	}
	return out, nil
}

const knowledgeColumns = `SELECT id, project, author, source_task, body, adopts, recorded_at`

func knowledgeByID(ctx context.Context, q querier, id int64) (Knowledge, error) {
	row, err := scanKnowledge(q.QueryRowContext(ctx, knowledgeColumns+` FROM knowledge WHERE id = ?`, id))
	if isNoRows(err) {
		return Knowledge{}, fmt.Errorf("%w: knowledge %d", ErrNotFound, id)
	}
	return row, err
}

func scanKnowledge(sc scanner) (Knowledge, error) {
	var row Knowledge
	var author, recorded string
	var source sql.NullString
	var adopts sql.NullInt64
	if err := sc.Scan(&row.ID, &row.Project, &author, &source, &row.Body, &adopts, &recorded); err != nil {
		return Knowledge{}, err
	}
	var err error
	if row.Author, err = knowledgeAuthorValue(author); err != nil {
		return Knowledge{}, err
	}
	if source.Valid {
		row.SourceTask = source.String
	}
	if adopts.Valid {
		row.Adopts = Known(adopts.Int64)
	}
	if row.RecordedAt, err = decodeTime(recorded); err != nil {
		return Knowledge{}, err
	}
	return row, nil
}
