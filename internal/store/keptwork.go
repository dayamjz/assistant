package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// KeptAnswer is one of PRD section 6's three answers to kept work. The set
// is closed and the table's check holds it too.
type KeptAnswer string

// The answers, in the PRD's order.
const (
	// KeptLand is "carry the work to the gate as a delivery".
	KeptLand KeptAnswer = "land"
	// KeptCarryOn is "relaunch a worker in that same copy".
	KeptCarryOn KeptAnswer = "carry-on"
	// KeptDiscard is "remove the copy", given by a person who was shown
	// what is lost.
	KeptDiscard KeptAnswer = "discard"
)

// KeptAnswers returns the answers in the PRD's order.
func KeptAnswers() []KeptAnswer { return []KeptAnswer{KeptLand, KeptCarryOn, KeptDiscard} }

// Recognized reports whether a is one of KeptAnswers.
func (a KeptAnswer) Recognized() bool {
	for _, known := range KeptAnswers() {
		if a == known {
			return true
		}
	}
	return false
}

// KeptClosure is how the program closed an entry nobody answered. The set
// is closed and holds one member, and none of its members is a discard.
type KeptClosure string

// KeptRemovedOnProof is the entry's copy removed by the cleanup decision on
// one of P12's proofs: its work was shown safe elsewhere after it was kept,
// a commit pushed to the upstream branch since among them. Only
// MarkCleanupRemoved and MarkRunCleanupRemoved write it, in the transaction
// that marks the removal, so nothing but a proven removal closes an entry
// this way.
const KeptRemovedOnProof KeptClosure = "removed-on-proof"

// KeptWork is one row of PRD section 8's kept work record: an isolated copy
// kept because its work has not landed, PRD principle P15's "kept work is
// never silent". It is written once, by RecordKeptWork, when the copy is
// kept. Its one revision closes it, once and only while it is open: a
// person's answer through AnswerKeptWork, or the program's closure,
// KeptRemovedOnProof, when the copy is removed on a proof. SurfacedAt, the
// PRD row's "when it last surfaced", is the one other field that moves,
// only forward, through MarkKeptWorkSurfaced. The schema's seals hold all of
// it.
//
// What it holds is what the copy was read to hold when it was kept, not
// what it holds now: a copy changed since is described by the entry a later
// cleanup writes for it, because the same path holding other commits is
// another entry.
//
// The gap: ForgetRepository removes a run's entry with its run whether or
// not it was answered, on run_cleanup's terms, because eject has removed the
// gate the copy was cut from by then. Refusing the eject over an unanswered
// entry is the strict reading of P15, and it waits on the answers being
// reachable, #411's second part.
type KeptWork struct {
	// ID is the row's identifier, which the store assigns and an answer
	// names.
	ID int64 `json:"id"`
	// TaskID names the task whose copy this is, for a task's copy; RunID
	// names the run, for a run's copy. Exactly one is known: it is the
	// owner the copy outlived.
	TaskID Optional[string] `json:"task_id"`
	RunID  Optional[string] `json:"run_id"`
	// RepositoryID is the project, as the owner's row named it when the
	// entry was written, and unknown for a task that names none.
	RepositoryID Optional[string] `json:"repository_id"`
	// Branch is the owner's branch when the entry was written, and unknown
	// for a task that names none.
	Branch Optional[string] `json:"branch"`
	// CopyPath is where the copy stands.
	CopyPath string `json:"copy_path"`
	// Holds is the commits the copy was read to hold, as the cleanup
	// decision examined them. For a task's copy that is every commit its
	// references and HEAD reach, the default branch's own commits among
	// them, so it is what the copy holds and not only what the default
	// branch lacks; Detail says what the proofs found of it.
	Holds []string `json:"holds"`
	// Reason is the cleanup decision's word for why the copy was kept. The
	// word is that decision's; nothing here reads it.
	Reason string `json:"reason"`
	// Detail is the decision's account, with credentials removed by the
	// store's redactor, as evidence.
	Detail string `json:"detail"`
	// KeptAt is when the entry was written.
	KeptAt time.Time `json:"kept_at"`
	// SurfacedAt is when the entry was last raised to someone, and unknown
	// until it was.
	SurfacedAt Optional[time.Time] `json:"surfaced_at"`
	// Answer is the answer, unknown until one is recorded.
	Answer Optional[KeptAnswer] `json:"answer"`
	// AnsweredBy is who answered, from Resolver's closed set; it has no
	// encoded form, on Resolver's terms, so it does not travel.
	AnsweredBy Optional[Resolver] `json:"-"`
	// AnsweredAt is when the answer was recorded.
	AnsweredAt Optional[time.Time] `json:"answered_at"`
	// Closure is the program's closure, unknown unless the copy was
	// removed on a proof; ClosedProof is that proof and ClosedAt when.
	Closure     Optional[KeptClosure]  `json:"closure"`
	ClosedProof Optional[CleanupProof] `json:"closed_proof"`
	ClosedAt    Optional[time.Time]    `json:"closed_at"`
}

// Answered reports whether the entry carries a person's answer.
func (k KeptWork) Answered() bool { return k.Answer.IsKnown() }

// Open reports whether the entry is still owed a decision: neither answered
// nor closed by the program.
func (k KeptWork) Open() bool { return !k.Answer.IsKnown() && !k.Closure.IsKnown() }

// KeptCopy is what a caller states when a copy is kept: whose it is, where
// it stands, what it was read to hold, and why it was kept. The project and
// the branch are not stated, because the owner's row is their owner and
// RecordKeptWork reads them there.
type KeptCopy struct {
	// TaskID or RunID names the owner; exactly one is set.
	TaskID string
	RunID  string
	// CopyPath is where the copy stands.
	CopyPath string
	// Holds is the commits the copy was read to hold. An empty list is a
	// known fact: a copy kept for an uncommitted change holds no commit of
	// its own.
	Holds []string
	// Reason and Detail are the decision's word and account.
	Reason string
	Detail string
}

// ErrKeptWorkAnswered is returned by AnswerKeptWork for an entry already
// answered. The first answer stands and nothing is written.
var ErrKeptWorkAnswered = errors.New("store: the kept work entry is already answered")

// ErrKeptWorkClosed is returned by AnswerKeptWork for an entry the program
// closed because its copy was removed on a proof. Nothing is written.
var ErrKeptWorkClosed = errors.New("store: the kept work entry was closed when its copy was removed on a proof")

// ErrUnknownKeptAnswer is returned by AnswerKeptWork for an answer outside
// KeptAnswers.
var ErrUnknownKeptAnswer = errors.New("store: not an answer to kept work")

// RecordKeptWork writes the kept work entry for a copy, or returns the one
// already written for the same copy holding the same commits, and reports
// whether this call wrote it. Owner, path and reason are required; an owner
// that is not there is ErrNotFound, wrapped. The project and the branch are
// read off the owner's row in the transaction that writes the entry, and
// the detail passes the store's redactor first.
//
// An entry already standing for the copy and its commits is returned as it
// stands, answered or not: a cleanup asked again of a copy nothing changed
// is the same kept work, and its answer, once given, is not asked again.
func (s *Store) RecordKeptWork(ctx context.Context, k KeptCopy) (KeptWork, bool, error) {
	switch {
	case (k.TaskID == "") == (k.RunID == ""):
		return KeptWork{}, false, errors.New("store: a kept work entry names exactly one owner, a task or a run")
	case strings.TrimSpace(k.CopyPath) == "":
		return KeptWork{}, false, errors.New("store: a kept work entry names no copy")
	case strings.TrimSpace(k.Reason) == "":
		return KeptWork{}, false, errors.New("store: a kept work entry names no reason")
	}
	holds, err := encodeExamined(k.Holds)
	if err != nil {
		return KeptWork{}, false, err
	}
	var out KeptWork
	written := false
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var repository, branch Optional[string]
		if k.TaskID != "" {
			err := tx.QueryRowContext(ctx, `SELECT repository_id, branch FROM task WHERE id = ?`, k.TaskID).Scan(&repository, &branch)
			if err != nil {
				return fmt.Errorf("task %s: %w", k.TaskID, errNoRows(err))
			}
		} else {
			var r, b string
			if err := tx.QueryRowContext(ctx, `SELECT repository_id, branch FROM run WHERE id = ?`, k.RunID).Scan(&r, &b); err != nil {
				return fmt.Errorf("run %s: %w", k.RunID, errNoRows(err))
			}
			repository, branch = Known(r), Known(b)
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO kept_work (task_id, run_id, repository_id, branch, copy_path, holds, reason, detail, kept_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (copy_path, holds) DO NOTHING`,
			optionalText(k.TaskID), optionalText(k.RunID), repository, branch, k.CopyPath, holds, k.Reason,
			s.redact.Redact(k.Detail), encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		written = n == 1
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE copy_path = ? AND holds = ?`,
			k.CopyPath, holds))
		return err
	})
	if err != nil {
		return KeptWork{}, false, fmt.Errorf("store: recording the kept work at %s: %w", k.CopyPath, err)
	}
	return out, written, nil
}

// KeptWorkEntry returns one entry by identifier, or ErrNotFound.
func (s *Store) KeptWorkEntry(ctx context.Context, id int64) (KeptWork, error) {
	if err := s.live(); err != nil {
		return KeptWork{}, err
	}
	k, err := scanKeptWork(s.read.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: reading kept work entry %d: %w", id, errNoRows(err))
	}
	return k, nil
}

// OpenKeptWork returns every entry still owed a decision, neither answered
// nor closed, oldest first. It is the read the supervisor loop, status and
// the catch-up read list kept work from.
func (s *Store) OpenKeptWork(ctx context.Context) ([]KeptWork, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, keptWorkColumns+` FROM kept_work WHERE answer IS NULL AND closure IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: reading the open kept work: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []KeptWork
	for rows.Next() {
		k, err := scanKeptWork(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading the open kept work: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the open kept work: %w", err)
	}
	return out, nil
}

// MarkKeptWorkSurfaced records that the entry was raised to someone at at.
// An earlier instant than the one recorded is refused by the schema's seal;
// an unknown identifier is ErrNotFound.
func (s *Store) MarkKeptWorkSurfaced(ctx context.Context, id int64, at time.Time) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE kept_work SET surfaced_at = ? WHERE id = ?`, encodeTime(at), id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: recording when kept work entry %d surfaced: %w", id, err)
	}
	return nil
}

// AnswerKeptWork records the entry's answer and who gave it, once. An
// answer outside KeptAnswers is ErrUnknownKeptAnswer, the zero Resolver is
// ErrNoResolver, an entry already answered is ErrKeptWorkAnswered, one the program closed is ErrKeptWorkClosed, and an
// unknown identifier is ErrNotFound; in each case nothing is written. It
// records and acts on nothing: what an answer does to the copy is its
// caller's, and who answered is recorded and never consulted, on
// Resolver's terms.
func (s *Store) AnswerKeptWork(ctx context.Context, id int64, a KeptAnswer, by Resolver) (KeptWork, error) {
	if !a.Recognized() {
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: %w: %q", id, ErrUnknownKeptAnswer, a)
	}
	who, err := resolverText(by)
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: answering kept work entry %d: %w", id, err)
	}
	var out KeptWork
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if k.Answered() {
			return ErrKeptWorkAnswered
		}
		if k.Closure.IsKnown() {
			return ErrKeptWorkClosed
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET answer = ?, answered_by = ?, answered_at = ? WHERE id = ?`,
			string(a), who, encodeTime(nowUTC()), id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: answering kept work entry %d: %w", id, err)
	}
	return out, nil
}

const keptWorkColumns = `SELECT id, task_id, run_id, repository_id, branch, copy_path, holds, reason, detail, kept_at,
	surfaced_at, answer, answered_by, answered_at, closure, closed_proof, closed_at`

func scanKeptWork(sc scanner) (KeptWork, error) {
	var k KeptWork
	var holds, kept string
	var surfaced, answer, by, answered, closure, proof, closed Optional[string]
	if err := sc.Scan(&k.ID, &k.TaskID, &k.RunID, &k.RepositoryID, &k.Branch, &k.CopyPath, &holds, &k.Reason, &k.Detail,
		&kept, &surfaced, &answer, &by, &answered, &closure, &proof, &closed); err != nil {
		return KeptWork{}, err
	}
	if err := json.Unmarshal([]byte(holds), &k.Holds); err != nil {
		return KeptWork{}, fmt.Errorf("column holds: %w", err)
	}
	var err error
	if k.KeptAt, err = decodeTime(kept); err != nil {
		return KeptWork{}, err
	}
	if k.SurfacedAt, err = optionalTimeValue(surfaced, "surfaced_at"); err != nil {
		return KeptWork{}, err
	}
	if a, ok := answer.Get(); ok {
		if !KeptAnswer(a).Recognized() {
			return KeptWork{}, fmt.Errorf("column answer: %w: %q", ErrUnknownKeptAnswer, a)
		}
		k.Answer = Known(KeptAnswer(a))
	}
	if k.AnsweredBy, err = resolverValue(by, "answered_by"); err != nil {
		return KeptWork{}, err
	}
	if k.AnsweredAt, err = optionalTimeValue(answered, "answered_at"); err != nil {
		return KeptWork{}, err
	}
	if c, ok := closure.Get(); ok {
		if KeptClosure(c) != KeptRemovedOnProof {
			return KeptWork{}, fmt.Errorf("column closure: not a closure of kept work: %q", c)
		}
		k.Closure = Known(KeptClosure(c))
	}
	if p, ok := proof.Get(); ok {
		k.ClosedProof = Known(CleanupProof(p))
	}
	if k.ClosedAt, err = optionalTimeValue(closed, "closed_at"); err != nil {
		return KeptWork{}, err
	}
	return k, nil
}

// closeKeptWorkOnRemovalTx closes, as KeptRemovedOnProof, every open entry
// the owner's copy has, inside the transaction that marks that copy's
// cleanup removed on proof. column is task_id or run_id, and nothing outside
// this package can name it.
func closeKeptWorkOnRemovalTx(ctx context.Context, tx *sql.Tx, column, owner, proof string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE kept_work SET closure = ?, closed_proof = ?, closed_at = ?
		WHERE `+column+` = ? AND answer IS NULL AND closure IS NULL`,
		string(KeptRemovedOnProof), proof, encodeTime(at), owner)
	if err != nil {
		return fmt.Errorf("closing the kept work of the removed copy: %w", err)
	}
	return nil
}

// optionalText is an empty string as an unknown column and anything else as
// itself.
func optionalText(s string) Optional[string] {
	if s == "" {
		return Unknown[string]()
	}
	return Known(s)
}
