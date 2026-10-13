package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// FollowUpKind is what a run did next with a path a decision-model answer was
// about. The three constants are the whole set, and AppendDecisionFollowUp
// refuses any other, because a kind no reader defined is a row nobody can
// count.
type FollowUpKind string

const (
	// FollowUpFinding is a later stage execution reporting a finding located
	// at the path. Detail is the finding's action.
	FollowUpFinding FollowUpKind = "finding"
	// FollowUpFix is a fix round whose commit touched the path. Detail is the
	// commit.
	FollowUpFix FollowUpKind = "fix"
	// FollowUpAnswer is a person's answer at a hold over a finding located at
	// the path. Detail is the option they chose.
	FollowUpAnswer FollowUpKind = "answer"
)

// recognized reports whether k is one of the three kinds.
func (k FollowUpKind) recognized() bool {
	return k == FollowUpFinding || k == FollowUpFix || k == FollowUpAnswer
}

// DecisionFollowUp is one thing a run did next with a path an answer was
// about. It is history on the same terms as DecisionAnswer: appended, never
// revised, and read back to decide nothing a run may do.
type DecisionFollowUp struct {
	// Seq is the row's position, assigned on append.
	Seq int64
	// Answer is the sequence of the DecisionAnswer this follows.
	Answer int64
	// Kind is what followed.
	Kind FollowUpKind
	// Stage is the stage it happened at.
	Stage string
	// Detail is what the kind carries, as handed over.
	Detail string
	// Recorded is when the row was appended, set by this package.
	Recorded time.Time
}

// LastDecisionAnswer returns the sequence of the latest answer recorded for
// run, and zero when there is none. A caller takes it before the work it is
// about to record follow-ups for and hands it to AppendDecisionFollowUp as
// through, so an answer that work itself produced is not followed by it.
func (s *Store) LastDecisionAnswer(ctx context.Context, run string) (int64, error) {
	if err := s.live(); err != nil {
		return 0, err
	}
	var last sql.NullInt64
	if err := s.read.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM decision_answer WHERE run = ?`, run).Scan(&last); err != nil {
		return 0, fmt.Errorf("store: reading the latest decision answer of run %s: %w", run, err)
	}
	return last.Int64, nil
}

// AppendDecisionFollowUp records that run did kind at stage with path, as a
// follow-up of every answer recorded for that run and path whose sequence is
// at most through, and returns how many answers it followed. A path no such
// answer was about is no error and records nothing, because there is nothing
// for it to follow.
//
// The rows are appended by one statement that also chooses the answers, so an
// answer recorded meanwhile is either followed or not, never half.
func (s *Store) AppendDecisionFollowUp(ctx context.Context, run, path string, kind FollowUpKind,
	stage, detail string, through int64) (int, error) {
	if strings.TrimSpace(run) == "" || strings.TrimSpace(path) == "" || strings.TrimSpace(stage) == "" {
		return 0, fmt.Errorf("store: a decision follow-up names no run, no path, or no stage")
	}
	if !kind.recognized() {
		return 0, fmt.Errorf("store: %q is not a decision follow-up kind", kind)
	}
	recorded := encodeTime(nowUTC())
	var followed int
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO decision_followup (answer, kind, stage, detail, recorded_at)
			SELECT seq, ?, ?, ?, ? FROM decision_answer
			WHERE run = ? AND path = ? AND seq <= ? ORDER BY seq`,
			string(kind), stage, detail, recorded, run, path, through)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		followed = int(n)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: appending a decision follow-up: %w", err)
	}
	return followed, nil
}

// DecisionFollowUps returns the follow-ups of one run's answers, in the order
// they were appended.
func (s *Store) DecisionFollowUps(ctx context.Context, run string) ([]DecisionFollowUp, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT f.seq, f.answer, f.kind, f.stage, f.detail, f.recorded_at
		FROM decision_followup f JOIN decision_answer a ON a.seq = f.answer
		WHERE a.run = ? ORDER BY f.seq`, run)
	if err != nil {
		return nil, fmt.Errorf("store: reading decision follow-ups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DecisionFollowUp
	for rows.Next() {
		var f DecisionFollowUp
		var kind, recorded string
		if err := rows.Scan(&f.Seq, &f.Answer, &kind, &f.Stage, &f.Detail, &recorded); err != nil {
			return nil, fmt.Errorf("store: reading decision follow-ups: %w", err)
		}
		f.Kind = FollowUpKind(kind)
		if f.Recorded, err = decodeTime(recorded); err != nil {
			return nil, fmt.Errorf("store: decision follow-up %d: %w", f.Seq, err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading decision follow-ups: %w", err)
	}
	return out, nil
}

// DecisionFiring is what the record says about one check in one run where it
// fired: the run, its status when read, how many of its answers to the check
// produced a finding, and how many of those anything followed.
type DecisionFiring struct {
	// Run is the run.
	Run string
	// Status is the run's status when the record was read.
	Status RunStatus
	// Fired is how many of the run's answers to the check produced a finding.
	Fired int
	// Followed is how many of those have at least one follow-up.
	Followed int
}

// DecisionFirings returns, for every run of repositoryID in which check
// produced at least one finding, how many answers did and how many of those
// anything followed, newest run first. An answer produced a finding when its
// recorded Finding is not empty, which is how the review stage records a
// firing; this package reads that and decides nothing from it.
func (s *Store) DecisionFirings(ctx context.Context, repositoryID, check string) ([]DecisionFiring, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT r.id, r.status, COUNT(*),
			SUM(CASE WHEN EXISTS (SELECT 1 FROM decision_followup f WHERE f.answer = a.seq) THEN 1 ELSE 0 END)
		FROM decision_answer a JOIN run r ON r.id = a.run
		WHERE r.repository_id = ? AND a.check_name = ? AND a.finding != ''
		GROUP BY r.id, r.status, r.created_at
		ORDER BY r.created_at DESC, r.id DESC`, repositoryID, check)
	if err != nil {
		return nil, fmt.Errorf("store: reading decision firings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []DecisionFiring
	for rows.Next() {
		var f DecisionFiring
		var status string
		if err := rows.Scan(&f.Run, &status, &f.Fired, &f.Followed); err != nil {
			return nil, fmt.Errorf("store: reading decision firings: %w", err)
		}
		f.Status = RunStatus(status)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading decision firings: %w", err)
	}
	return out, nil
}
