package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/home"
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
// (Unestablished) it is the one thing an open entry may later have filled
// in, once, through EstablishKeptWork, so an outage at the write does not
// leave the entry unestablished for good; an answered or closed entry is
// sealed whole, basis included.
//
// While an entry is open, a new task in its project is not dispatched:
// keptworkhold.go is that predicate and BeginDispatchAttempt its refusal.
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
	// LandingClaimedAt is when a landing claimed the entry, before it
	// started the run, through ClaimKeptWorkLanding; LandedRun is the run
	// it started, recorded by RecordKeptWorkLanding. An entry answered land
	// with no landed run is a landing not yet carried out: not yet claimed,
	// or claimed by a landing that stopped before it recorded its run.
	LandingClaimedAt Optional[time.Time] `json:"landing_claimed_at"`
	LandedRun        Optional[string]    `json:"landed_run"`
	// CarriedOnTask is the task a carry on answer creates, named with the
	// answer by AnswerKeptWorkCarryOn before its row is written, and
	// CarriedOnAt is when that task came to own the copy, written by
	// MarkKeptWorkCarriedOn. An entry answered carry-on with a task and no
	// instant is a carry on not yet carried out.
	CarriedOnTask Optional[string]    `json:"carried_on_task"`
	CarriedOnAt   Optional[time.Time] `json:"carried_on_at"`
	// DiscardDigest is the confirmation of what a discard answer was
	// shown, and DiscardLacks the commits it was shown as lost, nil for
	// an entry not answered discard; both are written with the answer by
	// AnswerKeptWorkDiscard. DiscardedAt is when the copy went, written by
	// MarkKeptWorkDiscarded. An entry answered discard with a digest and
	// no instant is a discard not yet carried out.
	DiscardDigest Optional[string]    `json:"discard_digest"`
	DiscardLacks  []string            `json:"discard_lacks"`
	DiscardedAt   Optional[time.Time] `json:"discarded_at"`
}

// Key is the name a person gives --hold to answer this entry, PRD section
// 9's hold answer: "kept-work/" and the entry's identifier. ParseKeptWorkKey
// is its one reader.
func (k KeptWork) Key() string { return KeptWorkKey(k.ID) }

// KeptWorkKey is Key for an entry identifier.
func KeptWorkKey(id int64) string { return keptWorkKeyPrefix + strconv.FormatInt(id, 10) }

const keptWorkKeyPrefix = "kept-work/"

// ParseKeptWorkKey reads an entry identifier out of a key Key wrote, and
// reports false for anything else, a hold's key among them.
func ParseKeptWorkKey(key string) (int64, bool) {
	rest, ok := strings.CutPrefix(key, keptWorkKeyPrefix)
	if !ok || rest == "" || strings.TrimLeft(rest, "0123456789") != "" {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
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

// ErrKeptWorkAnswered is returned by AnswerKeptWork and EstablishKeptWork
// for an entry already answered. The first answer stands and nothing is
// written.
var ErrKeptWorkAnswered = errors.New("store: the kept work entry is already answered")

// ErrKeptWorkClosed is returned by AnswerKeptWork and EstablishKeptWork
// for an entry the program closed because its copy was removed on a proof.
// Nothing is written.
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
		fmt.Fprintf(&b, " %s, the copy of %s at %s, holding %s (%s);", k.Key(), owner, k.CopyPath, holds, k.Reason)
	}
	b.WriteString(" answer each one (assistant --task or --run=RUN, --answer land and --hold with its kept-work/N key), " +
		"or wait for its work to land: once its commits are on the upstream branch or in the default branch, the next service start " +
		"removes the copy on that proof and closes the entry")
	return b.String()
}

// Unwrap makes the refusal match ErrKeptWorkOpen.
func (e *KeptWorkOpenError) Unwrap() error { return ErrKeptWorkOpen }

// refuseIfKeptWorkIsOpen is refuseUnlessForgettable's kept work check: every
// open entry whose project is the repository, read in the removal's
// transaction.
func refuseIfKeptWorkIsOpen(ctx context.Context, tx *sql.Tx, repositoryID string) error {
	rows, err := tx.QueryContext(ctx, openKeptWorkOfProject, repositoryID)
	if err != nil {
		return fmt.Errorf("store: checking the kept work of repository %s: %w", repositoryID, err)
	}
	open, err := scanKeptWorkRows(rows)
	if err != nil {
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

// EstablishKeptWork fills in the basis of an open entry written
// unestablished, once: Lacks and Against as RecordKeptWork would have taken
// them, with Unestablished kept as the account of the write. An entry
// already established is ErrKeptWorkEstablished, one a person answered is
// ErrKeptWorkAnswered and one the program closed is ErrKeptWorkClosed (the
// row is sealed whole from its one revision on, PRD section 8's "written
// once" and "its one revision is the answer"), an empty tip is refused, and
// an unknown identifier is ErrNotFound; in each case nothing is written.
// The schema holds the write to the same conditions, so a basis cannot be
// written past an answer by any path.
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
		switch {
		case k.Established():
			return ErrKeptWorkEstablished
		case k.Answered():
			return ErrKeptWorkAnswered
		case k.Closure.IsKnown():
			return ErrKeptWorkClosed
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

// KeptWorkOf returns the entry standing for a copy state, the path and the
// commits its references and HEAD stand on as RecordKeptWork keys them,
// answered or closed or not, or ErrNotFound. It is the read a caller makes
// to find what it would otherwise write, so that what an entry becomes can
// be decided before anything is written for the copy.
func (s *Store) KeptWorkOf(ctx context.Context, copyPath string, holds []string) (KeptWork, error) {
	if err := s.live(); err != nil {
		return KeptWork{}, err
	}
	encoded, err := encodeExamined(holds)
	if err != nil {
		return KeptWork{}, err
	}
	k, err := scanKeptWork(s.read.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE copy_path = ? AND holds = ?`,
		copyPath, encoded))
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: reading the kept work at %s: %w", copyPath, errNoRows(err))
	}
	return k, nil
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
	switch a {
	case KeptCarryOn:
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: a carry-on answer names the task carrying it on, through AnswerKeptWorkCarryOn", id)
	case KeptDiscard:
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: a discard answer records what it was shown, through AnswerKeptWorkDiscard", id)
	}
	return s.answerKeptWork(ctx, id, a, by, "", "", nil)
}

// AnswerKeptWorkDiscard is AnswerKeptWork for the discard answer, which
// records what the answer was shown in the same write, the digest of what
// the copy holds and would lose and the commits it was shown as lost: PRD
// section 6's "recording who discarded what". An empty digest is refused;
// lacks may be empty, and is recorded as shown. It records and removes
// nothing; the removal is the caller's, against both.
func (s *Store) AnswerKeptWorkDiscard(ctx context.Context, id int64, digest string, lacks []string, by Resolver) (KeptWork, error) {
	if strings.TrimSpace(digest) == "" {
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: a discard answer records what it was shown, and was shown nothing", id)
	}
	if lacks == nil {
		lacks = []string{}
	}
	return s.answerKeptWork(ctx, id, KeptDiscard, by, "", digest, lacks)
}

// AnswerKeptWorkCarryOn is AnswerKeptWork for the carry-on answer, which
// names the task that carries the entry on in the same write, and answers
// every other open entry of the same copy the same way in that write, since
// carrying on moves the whole copy: the task's
// identifier is reserved by the caller and its row may not exist yet, which
// is what lets a stop between this write and the task's creation be
// completed from the entry (P6). A task identifier home.ValidTaskID refuses
// is refused.
func (s *Store) AnswerKeptWorkCarryOn(ctx context.Context, id int64, task string, by Resolver) (KeptWork, error) {
	if err := home.ValidTaskID(task); err != nil {
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: the task carrying it on: %w", id, err)
	}
	return s.answerKeptWork(ctx, id, KeptCarryOn, by, task, "", nil)
}

// answerKeptWork is the one write of an answer, with the carry-on task
// beside a carry-on answer, the digest beside a discard, and nothing beside
// land.
func (s *Store) answerKeptWork(ctx context.Context, id int64, a KeptAnswer, by Resolver, task, digest string, shown []string) (KeptWork, error) {
	if !a.Recognized() {
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: %w: %q", id, ErrUnknownKeptAnswer, a)
	}
	who, err := resolverText(by)
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: answering kept work entry %d: %w", id, err)
	}
	var shownLacks Optional[string]
	if shown != nil {
		encoded, err := encodeExamined(shown)
		if err != nil {
			return KeptWork{}, err
		}
		shownLacks = Known(encoded)
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
		at := encodeTime(nowUTC())
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET answer = ?, answered_by = ?, answered_at = ?, carried_on_task = ?, discard_digest = ?, discard_lacks = ? WHERE id = ?`,
			string(a), who, at, optionalText(task), optionalText(digest), shownLacks, id); err != nil {
			return err
		}
		if a == KeptCarryOn {
			// Carrying on moves the whole copy, so every other open entry
			// of the same copy, the copy kept again at another state, is
			// carried on by the same task in this same write; none is left
			// holding the project's dispatch with its copy gone.
			if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET answer = ?, answered_by = ?, answered_at = ?, carried_on_task = ?
				WHERE copy_path = ? AND id <> ? AND answer IS NULL AND closure IS NULL`,
				string(a), who, at, task, k.CopyPath, id); err != nil {
				return err
			}
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
	surfaced_at, answer, answered_by, answered_at, closure, closed_proof, closed_at, lacks, against, unestablished,
	landing_claimed_at, landed_run, carried_on_task, carried_on_at, discard_digest, discard_lacks, discarded_at`

func scanKeptWork(sc scanner) (KeptWork, error) {
	var k KeptWork
	var holds, kept string
	var surfaced, answer, by, answered, closure, proof, closed, lacks, landingClaimed, carriedOn, discardLacks, discarded Optional[string]
	if err := sc.Scan(&k.ID, &k.TaskID, &k.RunID, &k.RepositoryID, &k.Branch, &k.CopyPath, &holds, &k.Reason, &k.Detail,
		&kept, &surfaced, &answer, &by, &answered, &closure, &proof, &closed, &lacks, &k.Against, &k.Unestablished,
		&landingClaimed, &k.LandedRun, &k.CarriedOnTask, &carriedOn, &k.DiscardDigest, &discardLacks, &discarded); err != nil {
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
	if encoded, ok := discardLacks.Get(); ok {
		if err := json.Unmarshal([]byte(encoded), &k.DiscardLacks); err != nil {
			return KeptWork{}, fmt.Errorf("column discard_lacks: %w", err)
		}
		if k.DiscardLacks == nil {
			k.DiscardLacks = []string{}
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
	if k.LandingClaimedAt, err = optionalTimeValue(landingClaimed, "landing_claimed_at"); err != nil {
		return KeptWork{}, err
	}
	if k.CarriedOnAt, err = optionalTimeValue(carriedOn, "carried_on_at"); err != nil {
		return KeptWork{}, err
	}
	if k.DiscardedAt, err = optionalTimeValue(discarded, "discarded_at"); err != nil {
		return KeptWork{}, err
	}
	return k, nil
}

// ErrKeptWorkLanded is returned by RecordKeptWorkLanding for an entry whose
// landing already names another run. The first run stands.
var ErrKeptWorkLanded = errors.New("store: the kept work entry's landing already names a run")

// ErrKeptWorkNotLand is returned by RecordKeptWorkLanding for an entry not
// answered land.
var ErrKeptWorkNotLand = errors.New("store: the kept work entry is not answered land")

// ErrKeptWorkLandingClaimed is returned by ClaimKeptWorkLanding for an entry
// whose landing is already claimed. The first claim stands.
var ErrKeptWorkLandingClaimed = errors.New("store: the kept work entry's landing is already claimed")

// ErrKeptWorkLandingUnclaimed is returned by RecordKeptWorkLanding for an
// entry whose landing was not claimed first.
var ErrKeptWorkLandingUnclaimed = errors.New("store: the kept work entry's landing was not claimed")

// ClaimKeptWorkLanding claims the entry's landing, once, before the run is
// started: an entry not answered land is ErrKeptWorkNotLand, one already
// claimed is ErrKeptWorkLandingClaimed, and an unknown identifier is
// ErrNotFound; in each case nothing is written. The claim is decided in the
// transaction that writes it, so two landings of one entry get one claim
// between them.
func (s *Store) ClaimKeptWorkLanding(ctx context.Context, id int64) (KeptWork, error) {
	var out KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if k.Answer.Or("") != KeptLand {
			return ErrKeptWorkNotLand
		}
		if k.LandingClaimedAt.IsKnown() {
			return ErrKeptWorkLandingClaimed
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET landing_claimed_at = ? WHERE id = ?`, encodeTime(nowUTC()), id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: claiming the landing of kept work entry %d: %w", id, err)
	}
	return out, nil
}

// RecordKeptWorkLanding records the run a land answer started, once: an
// entry not answered land is ErrKeptWorkNotLand, one whose landing was not
// claimed is ErrKeptWorkLandingUnclaimed, one whose landing names another
// run is ErrKeptWorkLanded, one that names this run already is accepted
// again and writes nothing, and an unknown identifier is ErrNotFound. It
// records and starts nothing.
func (s *Store) RecordKeptWorkLanding(ctx context.Context, id int64, run string) (KeptWork, error) {
	if strings.TrimSpace(run) == "" {
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: a landing names no run", id)
	}
	var out KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if k.Answer.Or("") != KeptLand {
			return ErrKeptWorkNotLand
		}
		if !k.LandingClaimedAt.IsKnown() {
			return ErrKeptWorkLandingUnclaimed
		}
		if landed, ok := k.LandedRun.Get(); ok {
			if landed != run {
				return fmt.Errorf("%w: run %s", ErrKeptWorkLanded, landed)
			}
			out = k
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET landed_run = ? WHERE id = ?`, run, id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: recording the landing of kept work entry %d: %w", id, err)
	}
	return out, nil
}

// KeptWorkLandingsOwed returns every entry answered land whose landing names
// no run yet, oldest first: a land answer a stopped service recorded and did
// not carry out, which the next start completes.
func (s *Store) KeptWorkLandingsOwed(ctx context.Context) ([]KeptWork, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, keptWorkColumns+` FROM kept_work WHERE answer = 'land' AND landed_run IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: reading the kept work landings owed: %w", err)
	}
	out, err := scanKeptWorkRows(rows)
	if err != nil {
		return nil, fmt.Errorf("store: reading the kept work landings owed: %w", err)
	}
	return out, nil
}

// ErrKeptWorkNotCarryOn is returned by MarkKeptWorkCarriedOn for an entry
// not answered carry-on.
var ErrKeptWorkNotCarryOn = errors.New("store: the kept work entry is not answered carry-on")

// ErrKeptWorkCarriedOn is returned by MarkKeptWorkCarriedOn for an entry
// already carried on.
var ErrKeptWorkCarriedOn = errors.New("store: the kept work entry is already carried on")

// MarkKeptWorkCarriedOn records that the task a carry-on answer named owns
// the copy, once, on the entry and every other entry that task carries on: an entry not answered carry-on is ErrKeptWorkNotCarryOn,
// one already marked is ErrKeptWorkCarriedOn, and an unknown identifier is
// ErrNotFound. It records and moves nothing.
func (s *Store) MarkKeptWorkCarriedOn(ctx context.Context, id int64) (KeptWork, error) {
	var out KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if k.Answer.Or("") != KeptCarryOn || !k.CarriedOnTask.IsKnown() {
			return ErrKeptWorkNotCarryOn
		}
		if k.CarriedOnAt.IsKnown() {
			return ErrKeptWorkCarriedOn
		}
		// Every entry the same task carries on, the copy's other entries the
		// answer took with it, is carried on with it.
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET carried_on_at = ? WHERE carried_on_task = ? AND carried_on_at IS NULL`,
			encodeTime(nowUTC()), k.CarriedOnTask.Or("")); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: marking kept work entry %d carried on: %w", id, err)
	}
	return out, nil
}

// KeptWorkCarryOnsOwed returns every entry answered carry-on whose task does
// not yet own the copy, oldest first: a carry on a stopped service recorded
// and did not carry out, which the next start completes.
func (s *Store) KeptWorkCarryOnsOwed(ctx context.Context) ([]KeptWork, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, keptWorkColumns+` FROM kept_work WHERE answer = 'carry-on' AND carried_on_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: reading the kept work carry-ons owed: %w", err)
	}
	out, err := scanKeptWorkRows(rows)
	if err != nil {
		return nil, fmt.Errorf("store: reading the kept work carry-ons owed: %w", err)
	}
	return out, nil
}

// ErrKeptWorkNotDiscard is returned by MarkKeptWorkDiscarded for an entry
// not answered discard.
var ErrKeptWorkNotDiscard = errors.New("store: the kept work entry is not answered discard")

// ErrKeptWorkDiscarded is returned by MarkKeptWorkDiscarded for an entry
// already discarded.
var ErrKeptWorkDiscarded = errors.New("store: the kept work entry is already discarded")

// MarkKeptWorkDiscarded records that the copy a discard answer was shown
// is gone, once: an entry not answered discard is ErrKeptWorkNotDiscard,
// one already marked is ErrKeptWorkDiscarded, and an unknown identifier is
// ErrNotFound. It records and removes nothing.
func (s *Store) MarkKeptWorkDiscarded(ctx context.Context, id int64) (KeptWork, error) {
	var out KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if k.Answer.Or("") != KeptDiscard || !k.DiscardDigest.IsKnown() {
			return ErrKeptWorkNotDiscard
		}
		if k.DiscardedAt.IsKnown() {
			return ErrKeptWorkDiscarded
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET discarded_at = ? WHERE id = ?`, encodeTime(nowUTC()), id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: marking kept work entry %d discarded: %w", id, err)
	}
	return out, nil
}

// KeptWorkDiscardsOwed returns every entry answered discard whose copy is
// not yet marked gone, oldest first: a discard a stopped service recorded
// and did not carry out, which the next start completes.
func (s *Store) KeptWorkDiscardsOwed(ctx context.Context) ([]KeptWork, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, keptWorkColumns+` FROM kept_work WHERE answer = 'discard' AND discarded_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: reading the kept work discards owed: %w", err)
	}
	out, err := scanKeptWorkRows(rows)
	if err != nil {
		return nil, fmt.Errorf("store: reading the kept work discards owed: %w", err)
	}
	return out, nil
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
