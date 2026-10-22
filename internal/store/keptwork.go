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
	// CarriedOnTask is the task a carry on creates, CarriedOnCopy the kept
	// copy's own identity as the carry on read it (opaque here:
	// internal/tasks' CopyIdentity says what it is), CarryOnBy who asked
	// and CarryOnClaimedAt when: the claim, written together on an open
	// entry by ClaimKeptWorkCarryOn before the task's row is written, which
	// leaves it open, and withdrawn together by WithdrawKeptWorkCarryOn
	// while it is unanswered and the task does not exist. CarriedOnAt is
	// when the task came to own the copy, written by MarkKeptWorkCarriedOn
	// in the one write that answers the entry carry-on from its claim, so
	// an entry answered carry-on is one whose task owns the copy, and an
	// open entry with a claim is a carry on under way or one a stopped
	// service left (CarryOnClaimed). A promotion names the promoted task
	// and no copy, since the copy never moves.
	CarriedOnTask    Optional[string]    `json:"carried_on_task"`
	CarriedOnCopy    Optional[string]    `json:"carried_on_copy"`
	CarryOnBy        Optional[Resolver]  `json:"-"`
	CarryOnClaimedAt Optional[time.Time] `json:"carry_on_claimed_at"`
	CarriedOnAt      Optional[time.Time] `json:"carried_on_at"`
	// DiscardDigest is a person's confirmation of a discard, the digest of
	// what they were shown, with DiscardLacks the commits it was shown as
	// lost (nil for an entry with no confirmation), DiscardBy who confirmed
	// and DiscardConfirmedAt when: written together on an open entry by
	// ConfirmKeptWorkDiscard, which leaves it open, and withdrawn together
	// by WithdrawKeptWorkDiscard while it is unanswered. DiscardedAt is
	// when the copy went, written by MarkKeptWorkDiscarded in the one
	// write that answers the entry discard from its confirmation, so an
	// entry answered discard is one whose copy is gone, and an open entry
	// with a confirmation is a discard under way or one a stopped service
	// left (DiscardConfirmed).
	DiscardDigest      Optional[string]    `json:"discard_digest"`
	DiscardLacks       []string            `json:"discard_lacks"`
	DiscardBy          Optional[Resolver]  `json:"-"`
	DiscardConfirmedAt Optional[time.Time] `json:"discard_confirmed_at"`
	DiscardedAt        Optional[time.Time] `json:"discarded_at"`
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

// DiscardConfirmed reports whether the entry is open with a person's
// discard confirmation on it: a discard recorded and not yet carried out,
// which is carried out or withdrawn, and until then holds the entry open.
func (k KeptWork) DiscardConfirmed() bool { return k.Open() && k.DiscardDigest.IsKnown() }

// CarryOnClaimed reports whether the entry is open with a carry-on claim on
// it: a carry on recorded and not yet carried out, which is carried out or
// withdrawn, and until then holds the entry open.
func (k KeptWork) CarryOnClaimed() bool { return k.Open() && k.CarriedOnTask.IsKnown() }

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

// ErrKeptWorkCopyOwed is returned by AnswerKeptWork, ClaimKeptWorkCarryOn,
// ConfirmKeptWorkDiscard and TransitionTaskShape for an entry of a copy
// that holds an answer not yet carried out: another entry's land whose run
// is not named, or a carry-on claim or a discard confirmation on any entry
// of the copy, this one included. Each of those still needs the copy
// where it stands, and a second answer could move it, remove it, or land
// it twice. Nothing is written; the owed answer is completed or withdrawn
// by its own next call or the next start, and the entry can be answered
// after.
var ErrKeptWorkCopyOwed = errors.New("store: another kept work entry of the same copy holds an answer not yet carried out")

// ErrKeptWorkDiscardConfirmed is returned, matching ErrKeptWorkCopyOwed
// too, by every answer and by ConfirmKeptWorkDiscard for an entry that
// already holds a discard confirmation: it stands until it is carried out
// or withdrawn, and nothing is written.
var ErrKeptWorkDiscardConfirmed = errors.New("store: the kept work entry already holds a discard confirmation")

// ErrKeptWorkCarryOnClaimed is returned, matching ErrKeptWorkCopyOwed too,
// by every answer and by ConfirmKeptWorkDiscard for an entry that already
// holds a carry-on claim: it stands until it is carried out or withdrawn,
// and nothing is written.
var ErrKeptWorkCarryOnClaimed = errors.New("store: the kept work entry already holds a carry-on claim")

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
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: a carry on is claimed through ClaimKeptWorkCarryOn and "+
			"answered with its adoption through MarkKeptWorkCarriedOn", id)
	case KeptDiscard:
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: a discard is confirmed through ConfirmKeptWorkDiscard and "+
			"answered with its removal through MarkKeptWorkDiscarded", id)
	}
	return s.answerKeptWork(ctx, id, a, by)
}

// ConfirmKeptWorkDiscard records a person's confirmation of a discard on
// an open entry, the first of the discard's two facts: digest, what the
// confirmation was shown, lacks, the commits it was shown as lost, and by,
// who confirmed, with the instant: PRD section 6's "recording who
// discarded what". The entry stays open, so it is listed and holds the
// project's dispatch as before, until MarkKeptWorkDiscarded answers it with
// the removal or WithdrawKeptWorkDiscard takes the confirmation back. An
// entry answered is ErrKeptWorkAnswered, one closed ErrKeptWorkClosed, one
// confirmed already ErrKeptWorkDiscardConfirmed, and one of a copy another
// entry of which holds an answer not yet carried out ErrKeptWorkCopyOwed;
// an empty digest and the zero Resolver are refused. Nothing is written on
// a refusal, and nothing is removed here.
func (s *Store) ConfirmKeptWorkDiscard(ctx context.Context, id int64, digest string, lacks []string, by Resolver) (KeptWork, error) {
	if strings.TrimSpace(digest) == "" {
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: a discard confirmation records what it was shown, and was shown nothing", id)
	}
	who, err := resolverText(by)
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: confirming a discard of kept work entry %d: %w", id, err)
	}
	if lacks == nil {
		lacks = []string{}
	}
	shown, err := encodeExamined(lacks)
	if err != nil {
		return KeptWork{}, err
	}
	var out KeptWork
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if err := refuseUnlessAnswerable(ctx, tx, k); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET discard_digest = ?, discard_lacks = ?, discard_by = ?, discard_confirmed_at = ?
			WHERE id = ?`, digest, shown, who, encodeTime(nowUTC()), id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: confirming a discard of kept work entry %d: %w", id, err)
	}
	return out, nil
}

// ErrKeptWorkNotConfirmed is returned by WithdrawKeptWorkDiscard for an
// entry that holds no discard confirmation, or one of another digest.
var ErrKeptWorkNotConfirmed = errors.New("store: the kept work entry holds no discard confirmation of that digest")

// WithdrawKeptWorkDiscard takes back the discard confirmation of digest from
// an entry still unanswered, which the caller does when the copy no longer
// holds what the confirmation was shown: the entry is open again with no
// confirmation, to be shown and answered afresh. An entry answered is
// ErrKeptWorkAnswered, and one with no confirmation of that digest
// ErrKeptWorkNotConfirmed. It removes nothing.
func (s *Store) WithdrawKeptWorkDiscard(ctx context.Context, id int64, digest string) (KeptWork, error) {
	var out KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if k.Answered() {
			return ErrKeptWorkAnswered
		}
		if k.DiscardDigest.Or("") != digest || digest == "" {
			return ErrKeptWorkNotConfirmed
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET discard_digest = NULL, discard_lacks = NULL, discard_by = NULL,
			discard_confirmed_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: withdrawing the discard confirmation of kept work entry %d: %w", id, err)
	}
	return out, nil
}

// ClaimKeptWorkCarryOn records a carry on of an open entry, the first of
// the carry on's two facts: task, the task that will carry it on, whose
// identifier the caller reserves and whose row does not exist yet, which
// is what lets a stop before its creation be completed from the entry
// (P6); copyIdentity, the kept copy's own identity as the caller read it,
// so the task adopts that copy and no other; and by, who asked, with the
// instant. The entry stays open, so it is listed and holds the project's
// dispatch as before, until MarkKeptWorkCarriedOn answers it with the
// adoption or WithdrawKeptWorkCarryOn takes the claim back. An entry
// answered is ErrKeptWorkAnswered, one closed ErrKeptWorkClosed, one
// holding a claim or a confirmation already, or of a copy another entry of
// which holds an answer not yet carried out, ErrKeptWorkCopyOwed; a task
// identifier home.ValidTaskID refuses, an empty copy identity and the zero
// Resolver are refused. Nothing is written on a refusal.
func (s *Store) ClaimKeptWorkCarryOn(ctx context.Context, id int64, task, copyIdentity string, by Resolver) (KeptWork, error) {
	if err := home.ValidTaskID(task); err != nil {
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: the task carrying it on: %w", id, err)
	}
	if strings.TrimSpace(copyIdentity) == "" {
		return KeptWork{}, fmt.Errorf("store: kept work entry %d: a carry on records which copy it carries on, and was given none", id)
	}
	who, err := resolverText(by)
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: claiming a carry on of kept work entry %d: %w", id, err)
	}
	var out KeptWork
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if err := refuseUnlessAnswerable(ctx, tx, k); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET carried_on_task = ?, carried_on_copy = ?, carry_on_by = ?, carry_on_claimed_at = ?
			WHERE id = ?`, task, copyIdentity, who, encodeTime(nowUTC()), id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: claiming a carry on of kept work entry %d: %w", id, err)
	}
	return out, nil
}

// ErrKeptWorkNotClaimed is returned by WithdrawKeptWorkCarryOn for an entry
// that holds no carry-on claim naming that task.
var ErrKeptWorkNotClaimed = errors.New("store: the kept work entry holds no carry-on claim naming that task")

// ErrCarryOnTaskStands is returned by WithdrawKeptWorkCarryOn when the task
// the claim names exists: the claim is being carried out, and is completed
// rather than withdrawn.
var ErrCarryOnTaskStands = errors.New("store: the task the carry-on claim names exists, so the claim is completed rather than withdrawn")

// WithdrawKeptWorkCarryOn takes back the carry-on claim naming task from an
// entry still unanswered, which the caller does when the task could not be
// created: the entry is open again with no claim, to be answered afresh.
// It is refused while the task's row exists (ErrCarryOnTaskStands), checked
// in the same transaction, so a withdrawal never leaves a task created for
// a carry on that no longer names it. An entry answered is
// ErrKeptWorkAnswered, and one with no claim naming task
// ErrKeptWorkNotClaimed. It moves nothing.
func (s *Store) WithdrawKeptWorkCarryOn(ctx context.Context, id int64, task string) (KeptWork, error) {
	var out KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		if k.Answered() {
			return ErrKeptWorkAnswered
		}
		if k.CarriedOnTask.Or("") != task || task == "" {
			return ErrKeptWorkNotClaimed
		}
		var stands int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task WHERE id = ?`, task).Scan(&stands); err != nil {
			return err
		}
		if stands > 0 {
			return ErrCarryOnTaskStands
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET carried_on_task = NULL, carried_on_copy = NULL, carry_on_by = NULL,
			carry_on_claimed_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
		out, err = scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		return err
	})
	if err != nil {
		return KeptWork{}, fmt.Errorf("store: withdrawing the carry-on claim of kept work entry %d: %w", id, err)
	}
	return out, nil
}

// answerKeptWork is the one write of a land answer; a carry on's answer is
// MarkKeptWorkCarriedOn's, written with its adoption, and a discard's
// MarkKeptWorkDiscarded's, written with its removal.
func (s *Store) answerKeptWork(ctx context.Context, id int64, a KeptAnswer, by Resolver) (KeptWork, error) {
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
		if err := refuseUnlessAnswerable(ctx, tx, k); err != nil {
			return err
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

// refuseUnlessAnswerable is the refusal every answer and every discard
// confirmation shares, inside its transaction: an entry answered, one
// closed, one holding a discard confirmation, and one of a copy another
// entry of which holds an answer not yet carried out are refused, on the
// errors' terms.
func refuseUnlessAnswerable(ctx context.Context, tx *sql.Tx, k KeptWork) error {
	if k.Answered() {
		return ErrKeptWorkAnswered
	}
	if k.Closure.IsKnown() {
		return ErrKeptWorkClosed
	}
	if k.DiscardDigest.IsKnown() {
		return fmt.Errorf("%w: %w", ErrKeptWorkCopyOwed, ErrKeptWorkDiscardConfirmed)
	}
	if k.CarriedOnTask.IsKnown() {
		return fmt.Errorf("%w: %w", ErrKeptWorkCopyOwed, ErrKeptWorkCarryOnClaimed)
	}
	var owed int64
	var what string
	switch err := tx.QueryRowContext(ctx, `SELECT id, CASE WHEN answer IS NULL AND discard_digest IS NOT NULL THEN 'confirmed discard'
		WHEN answer IS NULL THEN 'claimed carry on by task ' || carried_on_task ELSE 'answered ' || answer END
		FROM kept_work WHERE copy_path = ? AND id <> ? AND (`+owedOnCopy+`) ORDER BY id LIMIT 1`, k.CopyPath, k.ID).Scan(&owed, &what); {
	case err == nil:
		return fmt.Errorf("%w: entry %d is %s", ErrKeptWorkCopyOwed, owed, what)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	return nil
}

// owedOnCopy is the condition of an entry holding an answer not yet
// carried out, which ErrKeptWorkCopyOwed names: a land whose run is not
// named, and a carry on claimed or a discard confirmed on an entry neither
// answered nor closed.
const owedOnCopy = `(answer = 'land' AND landed_run IS NULL) OR
	(answer IS NULL AND closure IS NULL AND (carried_on_task IS NOT NULL OR discard_digest IS NOT NULL))`

const keptWorkColumns = `SELECT id, task_id, run_id, repository_id, branch, copy_path, holds, reason, detail, kept_at,
	surfaced_at, answer, answered_by, answered_at, closure, closed_proof, closed_at, lacks, against, unestablished,
	landing_claimed_at, landed_run, carried_on_task, carried_on_copy, carry_on_by, carry_on_claimed_at, carried_on_at, discard_digest,
	discard_lacks, discard_by, discard_confirmed_at, discarded_at`

func scanKeptWork(sc scanner) (KeptWork, error) {
	var k KeptWork
	var holds, kept string
	var surfaced, answer, by, answered, closure, proof, closed, lacks, landingClaimed, carryOnBy, carryOnClaimed, carriedOn, discardLacks,
		discardBy, confirmed, discarded Optional[string]
	if err := sc.Scan(&k.ID, &k.TaskID, &k.RunID, &k.RepositoryID, &k.Branch, &k.CopyPath, &holds, &k.Reason, &k.Detail,
		&kept, &surfaced, &answer, &by, &answered, &closure, &proof, &closed, &lacks, &k.Against, &k.Unestablished,
		&landingClaimed, &k.LandedRun, &k.CarriedOnTask, &k.CarriedOnCopy, &carryOnBy, &carryOnClaimed, &carriedOn, &k.DiscardDigest,
		&discardLacks, &discardBy, &confirmed, &discarded); err != nil {
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
	if k.CarryOnBy, err = resolverValue(carryOnBy, "carry_on_by"); err != nil {
		return KeptWork{}, err
	}
	if k.CarryOnClaimedAt, err = optionalTimeValue(carryOnClaimed, "carry_on_claimed_at"); err != nil {
		return KeptWork{}, err
	}
	if k.CarriedOnAt, err = optionalTimeValue(carriedOn, "carried_on_at"); err != nil {
		return KeptWork{}, err
	}
	if k.DiscardBy, err = resolverValue(discardBy, "discard_by"); err != nil {
		return KeptWork{}, err
	}
	if k.DiscardConfirmedAt, err = optionalTimeValue(confirmed, "discard_confirmed_at"); err != nil {
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
// that holds no carry-on claim.
var ErrKeptWorkNotCarryOn = errors.New("store: the kept work entry holds no carry-on claim")

// ErrKeptWorkCarriedOn is returned by MarkKeptWorkCarriedOn for an entry
// already carried on.
var ErrKeptWorkCarriedOn = errors.New("store: the kept work entry is already carried on")

// MarkKeptWorkCarriedOn records that the task a carry-on claim names owns
// the copy, the carry on's second fact, once: the entry is answered
// carry-on, by who asked and as of when, with the instant, in one write
// that also answers and marks every other open entry of the same copy kept
// by the time of the claim and holding no claim or confirmation of its
// own, the copy kept again at another state, with the same claim, since
// the copy moved for all of them. An entry kept after the claim was not
// what it carried on, and is left open. The task's row must exist; one
// that does not is ErrNotFound. An entry already marked is
// ErrKeptWorkCarriedOn, one answered otherwise ErrKeptWorkAnswered, one
// closed on a proof ErrKeptWorkClosed, one with no claim
// ErrKeptWorkNotCarryOn, and an unknown identifier ErrNotFound. It records
// and moves nothing else; the adoption is the caller's, made before this.
func (s *Store) MarkKeptWorkCarriedOn(ctx context.Context, id int64) (KeptWork, error) {
	var out KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		switch {
		case k.CarriedOnAt.IsKnown():
			return ErrKeptWorkCarriedOn
		case k.Answered():
			return ErrKeptWorkAnswered
		case k.Closure.IsKnown():
			return ErrKeptWorkClosed
		case !k.CarriedOnTask.IsKnown():
			return ErrKeptWorkNotCarryOn
		}
		var task, copied, by, claimed string
		if err := tx.QueryRowContext(ctx, `SELECT carried_on_task, carried_on_copy, carry_on_by, carry_on_claimed_at FROM kept_work WHERE id = ?`,
			id).Scan(&task, &copied, &by, &claimed); err != nil {
			return err
		}
		var stands int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task WHERE id = ?`, task).Scan(&stands); err != nil {
			return err
		}
		if stands == 0 {
			return fmt.Errorf("the task %s the claim names: %w", task, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET answer = 'carry-on', answered_by = ?, answered_at = ?, carried_on_task = ?,
			carried_on_copy = ?, carry_on_by = ?, carry_on_claimed_at = ?, carried_on_at = ?
			WHERE id = ? OR (copy_path = ? AND answer IS NULL AND closure IS NULL AND discard_digest IS NULL AND carried_on_task IS NULL
				AND kept_at <= ?)`,
			by, claimed, task, copied, by, claimed, encodeTime(nowUTC()), id, k.CopyPath, claimed); err != nil {
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

// KeptWorkCarryOnsOwed returns every entry holding a carry-on claim not yet
// carried out, open and oldest first: a carry on a stopped service claimed
// and did not carry out, which the next start completes or withdraws.
func (s *Store) KeptWorkCarryOnsOwed(ctx context.Context) ([]KeptWork, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, keptWorkColumns+` FROM kept_work
		WHERE carried_on_task IS NOT NULL AND answer IS NULL AND closure IS NULL ORDER BY id`)
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
// that holds no discard confirmation.
var ErrKeptWorkNotDiscard = errors.New("store: the kept work entry holds no discard confirmation")

// ErrKeptWorkDiscarded is returned by MarkKeptWorkDiscarded for an entry
// already discarded.
var ErrKeptWorkDiscarded = errors.New("store: the kept work entry is already discarded")

// MarkKeptWorkDiscarded records that the copy a discard confirmation was
// shown is gone, the discard's second fact, once: the entry is answered
// discard, by who confirmed and as of when, with the instant the copy
// went, in one write that also answers and marks every other open entry
// of the same copy kept by the time of the confirmation, the copy kept
// again at another state, with the same confirmation, since the copy is
// gone for all of them. An entry kept after the confirmation was not
// what it confirmed, and is left open. An entry already
// marked is ErrKeptWorkDiscarded, one answered otherwise
// ErrKeptWorkAnswered, one closed on a proof ErrKeptWorkClosed, one with no
// confirmation ErrKeptWorkNotDiscard, and an unknown identifier
// ErrNotFound. It records and removes nothing else; the removal is the
// caller's, made before this.
func (s *Store) MarkKeptWorkDiscarded(ctx context.Context, id int64) (KeptWork, error) {
	var out KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		k, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, id))
		if err != nil {
			return errNoRows(err)
		}
		switch {
		case k.DiscardedAt.IsKnown():
			return ErrKeptWorkDiscarded
		case k.Answered():
			return ErrKeptWorkAnswered
		case k.Closure.IsKnown():
			return ErrKeptWorkClosed
		case !k.DiscardDigest.IsKnown():
			return ErrKeptWorkNotDiscard
		}
		var digest, lacks, by, confirmed string
		if err := tx.QueryRowContext(ctx, `SELECT discard_digest, discard_lacks, discard_by, discard_confirmed_at FROM kept_work WHERE id = ?`,
			id).Scan(&digest, &lacks, &by, &confirmed); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET answer = 'discard', answered_by = ?, answered_at = ?, discard_digest = ?,
			discard_lacks = ?, discard_by = ?, discard_confirmed_at = ?, discarded_at = ?
			WHERE id = ? OR (copy_path = ? AND answer IS NULL AND closure IS NULL AND discard_digest IS NULL AND kept_at <= ?)`,
			by, confirmed, digest, lacks, by, confirmed, encodeTime(nowUTC()), id, k.CopyPath, confirmed); err != nil {
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

// KeptWorkDiscardsOwed returns every entry holding a discard confirmation
// not yet carried out, open and oldest first: a discard a stopped service
// confirmed and did not carry out, which the next start carries out or
// withdraws.
func (s *Store) KeptWorkDiscardsOwed(ctx context.Context) ([]KeptWork, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, keptWorkColumns+` FROM kept_work
		WHERE discard_digest IS NOT NULL AND answer IS NULL AND closure IS NULL ORDER BY id`)
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
