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
// another entry. Holds is that identity and is the copy's own state, the
// commits its references and HEAD stand on, so the same copy state is the
// same entry however the default branch read at the time. What of that the
// default branch lacks, Lacks against Against, is the entry's basis: it is
// written with the entry when it could be computed, and when it could not
// (Unestablished) it is the one thing an entry may later have filled in,
// once, through EstablishKeptWork, so an outage at the write does not leave
// the entry unestablished for good.
//
// A repository with an open entry cannot be forgotten: ForgetRepository,
// RepositoryRemovable and ReserveRepositoryRemoval refuse it with a
// *KeptWorkOpenError naming each entry. An entry is never refused because an
// eject is under way: eject reserves the removal, which stops new runs and so
// new run copies, and then refuses while any run copy of the repository
// still stands, decided or not, so the gate is never removed under a copy
// that an entry could be written for. A run's entry goes with its run only
// once it is answered or closed.
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
	// Holds is the commits the copy's references and HEAD stood on when it
	// was kept, refs/stash among them for a task's clone, and the commit
	// HEAD stood on for a run's worktree: the copy's own state, which is
	// the entry's identity, and not what of it is at risk, which Lacks
	// says. Each stands for everything it reaches.
	Holds []string `json:"holds"`
	// Lacks is what PRD section 8's row names, the commits the copy
	// reaches that the default branch at Against does not, each once; a
	// stash is uncommitted state and never among them. It is empty for a
	// copy kept only for a change, a file or a stash no commit records,
	// and nil when the entry is not Established.
	Lacks []string `json:"lacks"`
	// Against is the default branch tip Lacks was computed against, read
	// fresh and fetched by identifier: the upstream's for a task delivered
	// through the gate and for a run's copy, confirmed as the branch the
	// upstream's HEAD stands on; and the tip the repository's working copy
	// holds for its default branch for a task delivered under the local
	// posture, which is where that work lands and which may stand behind
	// or ahead of the upstream's. The task row's delivery column says
	// which, and nothing here records it twice. It is unknown while the
	// set is not established.
	Against Optional[string] `json:"against"`
	// Unestablished is why Lacks could not be computed when the entry was
	// written: the account of the read, the fetch or the comparison that
	// failed. It stays on an entry EstablishKeptWork later fills in, as
	// the account of the write. An entry written before the store recorded
	// a basis carries neither it nor Against and is as unestablished as one
	// that says why.
	Unestablished Optional[string] `json:"unestablished"`
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

// Established reports whether Lacks is known: the set the PRD row names,
// what the copy holds that the default branch lacks, computed against
// Against. An entry that is not established says what the copy stands on
// and nothing about what of it is safe elsewhere.
func (k KeptWork) Established() bool { return k.Against.IsKnown() }

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
	// Holds is the commits the copy's references and HEAD stand on, on
	// KeptWork.Holds' terms: the entry's identity.
	Holds []string
	// Lacks, Against and Unestablished are the basis, on KeptWork's terms.
	// Exactly one of Against and Unestablished is set; Lacks goes with
	// Against, and nil there is the known fact that nothing is lacking.
	Lacks         []string
	Against       string
	Unestablished string
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

// ErrRepositoryBeingRemoved is returned by CreateRun for a run of a
// repository whose removal ReserveRepositoryRemoval reserved: an eject that
// found the repository forgettable is under way, and a run would cut a copy
// under the gate it is about to remove. Nothing is written.
var ErrRepositoryBeingRemoved = errors.New("store: the repository is being removed, so no run of it is started")

// ErrKeptWorkOpen matches every *KeptWorkOpenError.
var ErrKeptWorkOpen = errors.New("store: the repository holds kept work nobody has decided")

// KeptWorkOpenError is ForgetRepository's and RepositoryRemovable's refusal
// of a repository that has open kept work entries, PRD principle P15's "the
// program never discards it on its own": forgetting the repository would
// take the gate the copies were cut from. It names each entry.
type KeptWorkOpenError struct {
	// Repository is the repository that was not forgotten.
	Repository string
	// Entries is every open entry of it, oldest first.
	Entries []KeptWork
}

// Error names each entry's copy, its owner and the commits it holds, and the
// way past the refusal in this build.
func (e *KeptWorkOpenError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "store: repository %s is not removed, because it holds kept work nobody has decided:", e.Repository)
	for _, k := range e.Entries {
		owner := "run " + k.RunID.Or("")
		if task, ok := k.TaskID.Get(); ok {
			owner = "task " + task
		}
		holds := "no commit of its own"
		if len(k.Holds) > 0 {
			holds = strings.Join(k.Holds, ", ")
		}
		fmt.Fprintf(&b, " entry %d, the copy of %s at %s, holding %s (%s);", k.ID, owner, k.CopyPath, holds, k.Reason)
	}
	b.WriteString(" until answering kept work ships (#411 part 2), the way past this is the work landing: once its commits are on " +
		"the upstream branch or in the default branch, the next service start removes the copy on that proof and closes the entry; " +
		"part 2's discard will be your other way")
	return b.String()
}

// Unwrap makes the refusal match ErrKeptWorkOpen.
func (e *KeptWorkOpenError) Unwrap() error { return ErrKeptWorkOpen }

// refuseIfKeptWorkIsOpen is refuseUnlessForgettable's kept work check: every
// open entry whose project is the repository, read in the removal's
// transaction.
func refuseIfKeptWorkIsOpen(ctx context.Context, tx *sql.Tx, repositoryID string) error {
	rows, err := tx.QueryContext(ctx, keptWorkColumns+` FROM kept_work
		WHERE repository_id = ? AND answer IS NULL AND closure IS NULL ORDER BY id`, repositoryID)
	if err != nil {
		return fmt.Errorf("store: checking the kept work of repository %s: %w", repositoryID, err)
	}
	defer func() { _ = rows.Close() }()
	var open []KeptWork
	for rows.Next() {
		k, err := scanKeptWork(rows)
		if err != nil {
			return fmt.Errorf("store: checking the kept work of repository %s: %w", repositoryID, err)
		}
		open = append(open, k)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: checking the kept work of repository %s: %w", repositoryID, err)
	}
	if len(open) > 0 {
		return &KeptWorkOpenError{Repository: repositoryID, Entries: open}
	}
	return nil
}

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
//
// The entry's basis is required too, exactly one of Against and
// Unestablished, and it is no part of what makes two entries the same: an
// entry already standing for the copy state is returned with the basis it
// has, and a caller that now has what it lacked fills it in through
// EstablishKeptWork.
func (s *Store) RecordKeptWork(ctx context.Context, k KeptCopy) (KeptWork, bool, error) {
	switch {
	case (k.TaskID == "") == (k.RunID == ""):
		return KeptWork{}, false, errors.New("store: a kept work entry names exactly one owner, a task or a run")
	case strings.TrimSpace(k.CopyPath) == "":
		return KeptWork{}, false, errors.New("store: a kept work entry names no copy")
	case strings.TrimSpace(k.Reason) == "":
		return KeptWork{}, false, errors.New("store: a kept work entry names no reason")
	case (strings.TrimSpace(k.Against) == "") == (strings.TrimSpace(k.Unestablished) == ""):
		return KeptWork{}, false, errors.New("store: a kept work entry names the default branch tip its commits were computed against, or why they could not be, and not both")
	}
	holds, err := encodeExamined(k.Holds)
	if err != nil {
		return KeptWork{}, false, err
	}
	lacks, err := encodeLacks(k.Against, k.Lacks)
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
		// A copy whose removal on a proof is already marked is not kept:
		// the entry would describe a copy that is gone with its work shown
		// safe, which a cleanup asked of it before the removal can race.
		cleanup := `SELECT removed_at FROM task_cleanup WHERE task_id = ?`
		owner := k.TaskID
		if k.RunID != "" {
			cleanup, owner = `SELECT removed_at FROM run_cleanup WHERE run = ?`, k.RunID
		}
		var removed Optional[string]
		switch err := tx.QueryRowContext(ctx, cleanup, owner).Scan(&removed); {
		case err == nil && removed.IsKnown():
			return fmt.Errorf("%w: the copy was removed on a proof", ErrCleanupRemoved)
		case err != nil && !isNoRows(err):
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO kept_work (task_id, run_id, repository_id, branch, copy_path, holds, reason, detail, kept_at,
				lacks, against, unestablished)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (copy_path, holds) DO NOTHING`,
			optionalText(k.TaskID), optionalText(k.RunID), repository, branch, k.CopyPath, holds, k.Reason,
			s.redact.Redact(k.Detail), encodeTime(nowUTC()), lacks, optionalText(k.Against),
			optionalText(s.redact.Redact(k.Unestablished)))
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

// encodeLacks is the lacks column for a basis: the list, empty for nil,
// when against is set, and NULL otherwise.
func encodeLacks(against string, lacks []string) (Optional[string], error) {
	if strings.TrimSpace(against) == "" {
		return Unknown[string](), nil
	}
	encoded, err := encodeExamined(lacks)
	if err != nil {
		return Unknown[string](), err
	}
	return Known(encoded), nil
}

// ErrKeptWorkEstablished is returned by EstablishKeptWork for an entry whose
// basis is already established. The first basis stands and nothing is
// written.
var ErrKeptWorkEstablished = errors.New("store: the kept work entry's basis is already established")

// EstablishKeptWork fills in the basis of an entry written unestablished,
// once: Lacks and Against as RecordKeptWork would have taken them, with
// Unestablished kept as the account of the write. An entry already
// established is ErrKeptWorkEstablished, an empty tip is refused, and an
// unknown identifier is ErrNotFound; in each case nothing is written. It
// is the one revision of an entry's basis, open or not: what the copy
// holds that the default branch lacks is a fact about the copy as it was
// kept, which an answer does not change, and the schema holds the write to
// that one direction.
func (s *Store) EstablishKeptWork(ctx context.Context, id int64, against string, lacks []string) (KeptWork, error) {
	if strings.TrimSpace(against) == "" {
		return KeptWork{}, fmt.Errorf("store: establishing kept work entry %d: no default branch tip was named", id)
	}
	encoded, err := encodeLacks(against, lacks)
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: establishing kept work entry %d: %w", id, err)
	}
	var out KeptWork
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if k.Established() {
			return ErrKeptWorkEstablished
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET lacks = ?, against = ? WHERE id = ?`, encoded, against, id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: establishing kept work entry %d: %w", id, err)
	}
	return out, nil
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
	surfaced_at, answer, answered_by, answered_at, closure, closed_proof, closed_at, lacks, against, unestablished`

func scanKeptWork(sc scanner) (KeptWork, error) {
	var k KeptWork
	var holds, kept string
	var surfaced, answer, by, answered, closure, proof, closed, lacks Optional[string]
	if err := sc.Scan(&k.ID, &k.TaskID, &k.RunID, &k.RepositoryID, &k.Branch, &k.CopyPath, &holds, &k.Reason, &k.Detail,
		&kept, &surfaced, &answer, &by, &answered, &closure, &proof, &closed, &lacks, &k.Against, &k.Unestablished); err != nil {
		return KeptWork{}, err
	}
	if err := json.Unmarshal([]byte(holds), &k.Holds); err != nil {
		return KeptWork{}, fmt.Errorf("column holds: %w", err)
	}
	if encoded, ok := lacks.Get(); ok {
		if err := json.Unmarshal([]byte(encoded), &k.Lacks); err != nil {
			return KeptWork{}, fmt.Errorf("column lacks: %w", err)
		}
		if k.Lacks == nil {
			k.Lacks = []string{}
		}
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
