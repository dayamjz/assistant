package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// RunStatus is where a run stands. It is the authoritative status, so it is
// written by the program that owns the run and read back by everyone else.
type RunStatus string

// The run statuses. A run is created pending, runs, and ends in exactly one of
// passed, failed, or terminated. Held is the state a run sits in while a hold
// waits on a person.
const (
	RunPending    RunStatus = "pending"
	RunRunning    RunStatus = "running"
	RunHeld       RunStatus = "held"
	RunPassed     RunStatus = "passed"
	RunFailed     RunStatus = "failed"
	RunTerminated RunStatus = "terminated"
)

// runStatuses is the closed set Recognized answers from, in the order
// RunStatuses reports. A status outside it is one no reader can interpret, so
// it is refused where a run would be moved to it rather than stored and read
// back later as a word nobody defined.
var runStatuses = []RunStatus{
	RunPending, RunRunning, RunHeld, RunPassed, RunFailed, RunTerminated,
}

// RunStatuses returns the closed set in the order above. The result is a copy,
// so a caller cannot add to the set by writing to it.
func RunStatuses() []RunStatus { return slices.Clone(runStatuses) }

// Recognized reports whether s is one of the statuses this package defines.
func (s RunStatus) Recognized() bool { return slices.Contains(runStatuses, s) }

// String renders the status as it is stored.
func (s RunStatus) String() string { return string(s) }

// Run is the authoritative record of one validation of one branch.
//
// The fields that are Optional are the ones a run does not have when it is
// created and may never acquire. A run that failed before it pushed has no push
// binding, and that is not the same fact as a push binding of the empty string.
type Run struct {
	// ID is the caller's identifier for this run.
	ID string `json:"id"`
	// RepositoryID names the repository the run validates.
	RepositoryID string `json:"repository_id"`
	// Branch is the branch that was pushed.
	Branch string `json:"branch"`
	// SubmittedHead is the commit the push carried, which is what P1 makes the
	// consent boundary.
	SubmittedHead string `json:"submitted_head"`
	// Base is the commit the branch is measured against.
	Base string `json:"base"`
	// CurrentHead is a branch tip SetRunHead recorded for the run, unknown
	// until one is. Nothing in this build calls SetRunHead outside tests and
	// nothing reads this field to decide anything: where a run's work stands
	// and what it pushed are its checkpoint's, which is what a rerun and
	// assistant sync read. The column stays because a migration may not drop
	// one. The json "-" tag keeps it off every answer that composes this
	// record, so no wire document carries a field that is null on every run;
	// a document that does carry the key is not read into this field.
	CurrentHead Optional[string] `json:"-"`
	// Status is where the run stands.
	Status RunStatus `json:"status"`
	// ApprovedCommit is the commit a completed review approved. It is unknown
	// until a review completes, and it is deliberately a separate fact from the
	// current head, because approving one commit says nothing about a later
	// one.
	ApprovedCommit Optional[string] `json:"approved_commit"`
	// PushBinding is what the run pushed and where, in the caller's own
	// notation. It is unknown until the run pushes. It is stored exactly as
	// given: the redactor runs only on the columns doc.go names as stored
	// redacted, and this is not one, so a caller that writes a credentialed
	// remote here has stored the credential.
	PushBinding Optional[string] `json:"push_binding"`
	// PullRequest is the pull request the run opened, in the caller's own
	// notation. It is unknown until one exists.
	PullRequest Optional[string] `json:"pull_request"`
	// FixerSession is the agent's opaque handle for the one durable fixer
	// session this run keeps across its fix rounds. It is unknown until a fix
	// round has reported one, and a run whose configuration asks for no
	// session reuse never acquires one.
	//
	// It is a reference rather than content: what it names lives with the
	// agent, and this column is what lets a restarted service continue the
	// same conversation instead of starting the run's fixer blind. It is
	// stored exactly as given, like every column but the repository URLs.
	FixerSession Optional[string] `json:"fixer_session"`
	// Intent is what the run was for.
	Intent string `json:"intent"`
	// IntentSource says where that intent came from, so a report can say who
	// asked rather than only what was asked.
	IntentSource string `json:"intent_source"`
	// Build is the software that produced this run.
	Build Build `json:"build"`
	// ConfigDigest identifies the configuration the run resolved, so a
	// surprising verdict can be traced to the settings that reached it as well
	// as to the code.
	ConfigDigest string `json:"config_digest"`
	// ConfigRejections lists the keys the run's own configuration resolution
	// dropped because the layer that set them was not allowed to, one rendered
	// line per rejection, in the order the resolution reported them. It is
	// recorded with ConfigDigest when the run's own resolution exists, so a
	// known empty list is a resolution that dropped nothing. It is unknown for
	// a run recorded before the column existed and for one refused before its
	// configuration resolved: the record understates rather than fabricating
	// an empty answer.
	ConfigRejections Optional[[]string] `json:"config_rejections"`
	// ResolvedAgent is the name of the agent the run resolved, as
	// configuration spells it. It is unknown for a run recorded before the
	// column existed and for one refused before an agent answered for it, so
	// the record understates here too.
	ResolvedAgent Optional[string] `json:"resolved_agent"`
	// Park is which loop bound parked the run, known exactly when
	// TransitionRunOnPark ended it. It is what tells a terminated run a bound
	// stopped from one a person cancelled or a push superseded, which the
	// status alone cannot. It is unknown for every run no bound parked and for
	// a run parked before the column existed, whose park only its checkpoint
	// still names: the record understates rather than guessing.
	Park Optional[RunPark] `json:"park"`
	// StandingConsent is when the run was put under the standing consent PRD
	// section 9 describes, known exactly when GiveRunStandingConsent recorded
	// it. It is unknown for every run nobody gave consent to and for a run
	// recorded before the column existed, which is the same fact: no
	// decision of that run was answered on consent. Once known it is never
	// cleared, so the record of a run whose decisions the program answered
	// keeps saying so. Which decisions it answered, and that each was the
	// program's, is the hold record ResolvedByStandingConsent names.
	StandingConsent Optional[time.Time] `json:"standing_consent"`
	// NoCodeHost says the run's repository had no code host when the run
	// began: its upstream named no network host, so the run passes over the
	// pull request and checks stages and ends with its own outcome, issue
	// #283's owner decision. MarkRunNoCodeHost writes it once, before the run
	// starts. It is false for a run of a repository with a code host and for
	// a run recorded before the column existed, and it is a fact about the
	// repository as the run read it, not about what any stage did.
	NoCodeHost bool `json:"no_code_host,omitempty"`
	// HeldMoves is how many times the run has moved into held, which
	// transitionRun counts in the statement that makes each move, so the
	// count that names a hold is durable with the hold itself. It is what
	// keys the supervisor loop's wake for a run moved to held, one wake per
	// hold, read off this row rather than off any history (P8). It is
	// unknown for a run recorded before the column existed until its first
	// move into held after that, which counts as one. The json "-" tag keeps
	// it off every answer that composes this record: it identifies a hold for
	// the loop and is nothing a reader decides from.
	HeldMoves Optional[int64] `json:"-"`
	// CreatedAt is when the run was recorded, which PRD section 8 requires to
	// precede the creation of its directory.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is when the record last changed.
	UpdatedAt time.Time `json:"updated_at"`
}

// ParkBound names which of PRD section 5's three loop bounds parked a run. The
// set is closed: a park this package cannot name is refused where it would be
// recorded rather than stored as a word nobody defined.
type ParkBound string

// The three bounds.
const (
	// ParkRoundLimit is a stage's automatic fix round limit: the stage still
	// reported findings to fix when the limit was reached.
	ParkRoundLimit ParkBound = "round_limit"
	// ParkConvergence is a fix round that left the run's state exactly where
	// the round before it left it.
	ParkConvergence ParkBound = "convergence"
	// ParkStepBudget is the run-wide step budget.
	ParkStepBudget ParkBound = "step_budget"
)

// parkBounds is the closed set ParkBounds reports and Recognized answers from.
var parkBounds = []ParkBound{ParkRoundLimit, ParkConvergence, ParkStepBudget}

// ParkBounds returns the closed set. The result is a copy.
func ParkBounds() []ParkBound { return slices.Clone(parkBounds) }

// Recognized reports whether b is one of the three bounds.
func (b ParkBound) Recognized() bool { return slices.Contains(parkBounds, b) }

// RunPark is the record of which bound parked a run and where, written once
// with the move that ends the run. Every field is read off the checkpoint the
// park was recorded from, by the caller recording it: this package checks the
// record's shape and decides nothing about what a bound means.
type RunPark struct {
	// Bound is which bound parked the run.
	Bound ParkBound `json:"bound"`
	// Stage is the stage whose loop the bound was counting, or, for the
	// budget, the stage the run stood in front of. It is empty when the
	// checkpoint named no stage, which the step budget may do and the other
	// two may not.
	Stage string `json:"stage,omitempty"`
	// Node is the graph node the run stands in front of, which has not run.
	Node string `json:"node"`
	// Limit is the bound's value: the round limit for ParkRoundLimit and the
	// step budget for ParkStepBudget. A convergence park has no limit, and
	// carries zero, and so does a round limit park whose checkpoint was
	// written before checkpoints recorded the parking edge, whose limit
	// nothing recorded.
	Limit int `json:"limit,omitempty"`
	// Checkpoint is the sequence number of the checkpoint the park was read
	// off, which is where the run's state as it parked, the stage reports
	// among it, is kept.
	Checkpoint int `json:"checkpoint"`
}

// Validate refuses a record whose shape no park has: an unrecognized bound,
// no node, no checkpoint, a round limit or a convergence park with no stage,
// a step budget below one, a negative round limit, or a limit on a
// convergence park.
func (p RunPark) Validate() error {
	if !p.Bound.Recognized() {
		return fmt.Errorf("store: %q is not a bound that parks a run", p.Bound)
	}
	if strings.TrimSpace(p.Node) == "" {
		return fmt.Errorf("store: a %s park names no node", p.Bound)
	}
	if p.Checkpoint < 1 {
		return fmt.Errorf("store: a %s park names checkpoint %d, which no run wrote", p.Bound, p.Checkpoint)
	}
	switch p.Bound {
	case ParkRoundLimit, ParkConvergence:
		if strings.TrimSpace(p.Stage) == "" {
			return fmt.Errorf("store: a %s park names no stage", p.Bound)
		}
	case ParkStepBudget:
	}
	switch p.Bound {
	case ParkRoundLimit:
		if p.Limit < 0 {
			return fmt.Errorf("store: a %s park carries a limit of %d", p.Bound, p.Limit)
		}
	case ParkStepBudget:
		if p.Limit < 1 {
			return fmt.Errorf("store: a %s park carries a limit of %d", p.Bound, p.Limit)
		}
	case ParkConvergence:
		if p.Limit != 0 {
			return fmt.Errorf("store: a convergence park carries a limit of %d, and convergence has none", p.Limit)
		}
	}
	return nil
}

// TransitionRunOnPark moves a run to terminated and records the park that
// ended it, in one transaction, on TransitionRun's terms: from names every
// status the move is legal out of, and a run already terminated is returned
// as it stands with nothing written, park included, so a run a person ended
// before its park was recorded keeps reading as the ending it had.
//
// It refuses a park Validate refuses before anything is written. The column
// is written once: the schema refuses a second write, so a run's park is
// never revised.
func (s *Store) TransitionRunOnPark(ctx context.Context, id string, from []RunStatus, park RunPark) (Run, error) {
	if err := park.Validate(); err != nil {
		return Run{}, fmt.Errorf("store: run %s: %w", id, err)
	}
	// Encoding a struct of strings and integers cannot fail.
	encoded, _ := json.Marshal(park)
	return s.transitionRun(ctx, id, from, RunTerminated, func(tx *sql.Tx, now string) error {
		if _, err := tx.ExecContext(ctx, `UPDATE run SET park = ? WHERE id = ?`, string(encoded), id); err != nil {
			return fmt.Errorf("store: recording what parked run %s: %w", id, err)
		}
		return nil
	})
}

// CreateRun records a new run and returns it as stored.
//
// It refuses a run that names no build with ErrBuildIdentityMissing and one
// that names no configuration with ErrConfigDigestMissing. PRD section 8
// requires a surprising result to be traceable to both, and a run recorded
// without them is a report nobody can trace, discovered long after the run that
// would have explained it.
func (s *Store) CreateRun(ctx context.Context, r Run) (Run, error) {
	if strings.TrimSpace(r.ID) == "" {
		return Run{}, fmt.Errorf("store: run has no identifier")
	}
	if strings.TrimSpace(r.Branch) == "" {
		return Run{}, fmt.Errorf("store: run %s has no branch", r.ID)
	}
	if strings.TrimSpace(r.SubmittedHead) == "" {
		return Run{}, fmt.Errorf("store: run %s has no submitted head", r.ID)
	}
	if err := r.Build.Validate(); err != nil {
		return Run{}, fmt.Errorf("store: run %s: %w", r.ID, err)
	}
	if strings.TrimSpace(r.ConfigDigest) == "" {
		return Run{}, fmt.Errorf("store: run %s: %w", r.ID, ErrConfigDigestMissing)
	}
	if r.Status == "" {
		r.Status = RunPending
	}

	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO run (
				id, repository_id, branch, submitted_head, base, current_head, status,
				approved_commit, push_binding, pull_request, fixer_session, intent, intent_source,
				build_version, build_revision, build_modified, build_go, config_digest,
				config_rejections, resolved_agent,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.RepositoryID, r.Branch, r.SubmittedHead, r.Base, r.CurrentHead, string(r.Status),
			r.ApprovedCommit, r.PushBinding, r.PullRequest, r.FixerSession, r.Intent, r.IntentSource,
			r.Build.Version, r.Build.Revision, boolToInt(r.Build.Modified), r.Build.Go, r.ConfigDigest,
			rejectionsText(r.ConfigRejections), r.ResolvedAgent,
			encodeTime(now), encodeTime(now))
		return err
	})
	if err != nil {
		return Run{}, fmt.Errorf("store: creating run %s: %w", r.ID, err)
	}
	return s.Run(ctx, r.ID)
}

// Run returns the run with the given identifier, or ErrNotFound.
func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	if err := s.live(); err != nil {
		return Run{}, err
	}
	row := s.read.QueryRowContext(ctx, runColumns+` FROM run WHERE id = ?`, id)
	r, err := scanRun(row)
	if err != nil {
		return Run{}, fmt.Errorf("store: reading run %s: %w", id, errNoRows(err))
	}
	return r, nil
}

// RunsForRepository returns every run of one repository, newest first.
func (s *Store) RunsForRepository(ctx context.Context, repositoryID string) ([]Run, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx,
		runColumns+` FROM run WHERE repository_id = ? ORDER BY created_at DESC, id DESC`, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("store: listing runs of repository %s: %w", repositoryID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing runs of repository %s: %w", repositoryID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing runs of repository %s: %w", repositoryID, err)
	}
	return out, nil
}

// TransitionRun moves a run to a status, and does it anchored to what the
// caller believed the run's status was: from names every status the move is
// legal out of, and the move is refused when the run is in none of them.
//
// Reading the status and writing the new one happen in one transaction on the
// single writer connection, so two callers moving one run cannot both find it
// where they expected it and both write. That is what makes this the mechanism
// a lifecycle rule can be built on: a caller that read the status first and
// then wrote would be deciding against a status that may already have changed.
// Which moves are legal is not decided here. This package records what a run
// is and does not decide what a run may do next, so the set of statuses a move
// may be made out of comes from the caller with the move.
//
// A run already in the status it is being moved to is left exactly as it
// stands: nothing is written, updated_at does not move, and the run comes back
// unchanged with no error. So a transition retried after a failure that had
// already committed reports the state it established rather than a refusal,
// and from never has to name the destination to say so.
//
// It refuses an unknown run with ErrNotFound, a destination that is not one of
// the statuses above, an empty from, and a run in a status from does not name.
// That last refusal is a *RunStatusError naming what the run was actually in,
// so a caller can report the state it found rather than only that it was
// surprised.
func (s *Store) TransitionRun(ctx context.Context, id string, from []RunStatus, to RunStatus) (Run, error) {
	return s.transitionRun(ctx, id, from, to, nil)
}

// transitionRun is the one transaction behind both transitions. also, when it
// is not nil, runs inside the transaction after the status has moved and
// before the row is read back, with the instant the move was stamped with, so
// a record that has to describe the same ending as the move is written under
// the same commit. It does not run when nothing moved.
func (s *Store) transitionRun(ctx context.Context, id string, from []RunStatus, to RunStatus,
	also func(tx *sql.Tx, now string) error) (Run, error) {
	if !to.Recognized() {
		return Run{}, fmt.Errorf("store: run %s: %q is not a run status", id, to)
	}
	if len(from) == 0 {
		return Run{}, fmt.Errorf("store: run %s: a transition to %s names no status it may be made from", id, to)
	}
	var moved Run
	// Every refusal below says what it is on its own, so nothing wraps this
	// call as a whole: a *RunStatusError already names the run, where it
	// stands, and where it was to go, and wrapping it would print all three
	// twice.
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := scanRun(tx.QueryRowContext(ctx, runColumns+` FROM run WHERE id = ?`, id))
		if err != nil {
			return fmt.Errorf("store: reading run %s: %w", id, errNoRows(err))
		}
		if current.Status == to {
			moved = current
			return nil
		}
		if !slices.Contains(from, current.Status) {
			return &RunStatusError{Run: id, Expected: slices.Clone(from), Actual: current.Status, To: to}
		}
		now := encodeTime(nowUTC())
		// A move into held counts itself in the same statement, so the hold
		// and the count that names it are one write; run_held_moves'
		// trigger refuses the column changing any other way.
		move := `UPDATE run SET status = ?, updated_at = ? WHERE id = ?`
		if to == RunHeld {
			move = `UPDATE run SET status = ?, updated_at = ?, held_moves = coalesce(held_moves, 0) + 1 WHERE id = ?`
		}
		if _, err := tx.ExecContext(ctx, move, string(to), now, id); err != nil {
			return fmt.Errorf("store: moving run %s to %s: %w", id, to, err)
		}
		if also != nil {
			if err := also(tx, now); err != nil {
				return err
			}
		}
		// Read back rather than assembling the answer, so what a caller is
		// handed is the row as it now stands and not this function's account
		// of what it should be.
		if moved, err = scanRun(tx.QueryRowContext(ctx, runColumns+` FROM run WHERE id = ?`, id)); err != nil {
			return fmt.Errorf("store: reading run %s after moving it to %s: %w", id, to, err)
		}
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	return moved, nil
}

// SetRunFixerSession records the agent's handle for the run's durable fixer
// session, which is what lets a later fix round of the same run continue the
// same conversation after the process that opened it is gone.
//
// It records a reference the caller was given. Nothing here opens, validates,
// or reasons about a session: what the handle means is the agent adapter's,
// and which invocations may carry one is internal/agents' type split.
func (s *Store) SetRunFixerSession(ctx context.Context, id, reference string) error {
	return s.updateRun(ctx, id, "fixer_session", reference)
}

// SetRunHead records a branch tip for the run in CurrentHead. It has no
// caller in this build outside tests; CurrentHead says why.
func (s *Store) SetRunHead(ctx context.Context, id, head string) error {
	return s.updateRun(ctx, id, "current_head", head)
}

// SetRunApprovedCommit records the commit a completed review approved.
func (s *Store) SetRunApprovedCommit(ctx context.Context, id, commit string) error {
	return s.updateRun(ctx, id, "approved_commit", commit)
}

// SetRunPushBinding records what the run pushed and where.
func (s *Store) SetRunPushBinding(ctx context.Context, id, binding string) error {
	return s.updateRun(ctx, id, "push_binding", binding)
}

// SetRunPullRequest records the pull request the run opened.
func (s *Store) SetRunPullRequest(ctx context.Context, id, pullRequest string) error {
	return s.updateRun(ctx, id, "pull_request", pullRequest)
}

// SetRunConfigResolution replaces the digest identifying the configuration the
// run resolved and records the keys that resolution rejected, in one write.
//
// A run's record is created before its repository's configuration copies are
// read, because PRD section 8 orders the row before the run's directory and
// the copies are read from repositories that fetch, so the digest a run is
// created with covers what was known then. This is how the record catches up
// once the run's own resolution exists, keeping PRD section 8's traceability
// about what the run actually resolved rather than about a placeholder.
//
// The rejections travel with the digest because they are two facts about one
// resolution, and a write that carried one without the other could leave the
// record describing a digest with another resolution's rejections beside it. A
// nil or empty rejected records "this resolution dropped nothing", which is a
// different fact from the unknown a run refused before resolution keeps.
//
// It refuses an empty digest, so a run cannot be made untraceable after the
// fact, and an unknown run with ErrNotFound.
func (s *Store) SetRunConfigResolution(ctx context.Context, id, digest string, rejected []string) error {
	if strings.TrimSpace(digest) == "" {
		return fmt.Errorf("store: run %s: config_digest is empty", id)
	}
	encoded := encodeRejections(rejected)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE run SET config_digest = ?, config_rejections = ?, updated_at = ? WHERE id = ?`,
			digest, encoded, encodeTime(nowUTC()), id)
		if err != nil {
			return fmt.Errorf("store: updating run %s: %w", id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: updating run %s: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("store: updating run %s: %w", id, ErrNotFound)
		}
		return nil
	})
}

// SetRunResolvedAgent records the name of the agent the run resolved, as
// configuration spells it. It is a name and not a second record of the agent:
// what the name means is the agent catalog's, and internal/agents owns the
// resolution itself.
func (s *Store) SetRunResolvedAgent(ctx context.Context, id, name string) error {
	return s.updateRun(ctx, id, "resolved_agent", name)
}

// MarkRunNoCodeHost records that the run's repository has no code host. It
// is written while the run is still pending, which the schema enforces, so a
// run that began with a code host cannot acquire the fact afterwards. A run
// already marked is left as it is and answers nil, because the fact recorded
// twice is the one fact; a run that does not exist is ErrNotFound.
//
// It records and decides nothing: what counts as no code host is
// internal/forge's ReachOf, and what a run does about it is
// internal/pipeline's.
func (s *Store) MarkRunNoCodeHost(ctx context.Context, id string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE run SET no_code_host = 1, updated_at = ? WHERE id = ? AND no_code_host IS NULL`,
			encodeTime(nowUTC()), id)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 1 {
			return nil
		}
		var exists int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM run WHERE id = ?`, id).Scan(&exists); {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: recording that run %s has no code host: %w", id, err)
	}
	return nil
}

// GiveRunStandingConsent records that the run was put under standing consent,
// now, and returns the run as stored. A run already given it keeps the
// instant it was first given and is returned unchanged, because consent given
// twice is the one consent rather than a second; a run that does not exist is
// ErrNotFound.
//
// It records and decides nothing. Whether a repository allows standing
// consent, whether a run may still be given it, and what the program then
// answers are internal/service's, and the migration that added the column
// keeps it from ever being cleared.
func (s *Store) GiveRunStandingConsent(ctx context.Context, id string) (Run, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now := encodeTime(nowUTC())
		result, err := tx.ExecContext(ctx,
			`UPDATE run SET standing_consent = ?, updated_at = ? WHERE id = ? AND standing_consent IS NULL`,
			now, now, id)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 1 {
			return nil
		}
		var exists int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM run WHERE id = ?`, id).Scan(&exists); {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		}
		return nil
	})
	if err != nil {
		return Run{}, fmt.Errorf("store: giving run %s standing consent: %w", id, err)
	}
	return s.Run(ctx, id)
}

// updateRun writes one column of one run. The column name is never a caller's
// string: it comes from the fixed set above, so no accessor composes SQL from
// input.
func (s *Store) updateRun(ctx context.Context, id, column, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("store: run %s: %s is empty", id, column)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE run SET `+column+` = ?, updated_at = ? WHERE id = ?`,
			value, encodeTime(nowUTC()), id)
		if err != nil {
			return fmt.Errorf("store: updating run %s: %w", id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: updating run %s: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("store: updating run %s: %w", id, ErrNotFound)
		}
		return nil
	})
}

const runColumns = `SELECT
	id, repository_id, branch, submitted_head, base, current_head, status,
	approved_commit, push_binding, pull_request, fixer_session, intent, intent_source,
	build_version, build_revision, build_modified, build_go, config_digest,
	config_rejections, resolved_agent, park, standing_consent, held_moves, no_code_host,
	created_at, updated_at`

func scanRun(sc scanner) (Run, error) {
	var r Run
	var status, created, updated string
	var modified int
	var rejections, park, consent Optional[string]
	var noCodeHost Optional[int64]
	if err := sc.Scan(
		&r.ID, &r.RepositoryID, &r.Branch, &r.SubmittedHead, &r.Base, &r.CurrentHead, &status,
		&r.ApprovedCommit, &r.PushBinding, &r.PullRequest, &r.FixerSession, &r.Intent, &r.IntentSource,
		&r.Build.Version, &r.Build.Revision, &modified, &r.Build.Go, &r.ConfigDigest,
		&rejections, &r.ResolvedAgent, &park, &consent, &r.HeldMoves, &noCodeHost,
		&created, &updated,
	); err != nil {
		return Run{}, err
	}
	r.Status = RunStatus(status)
	r.Build.Modified = modified != 0
	if marked, ok := noCodeHost.Get(); ok && marked == 1 {
		r.NoCodeHost = true
	}
	var err error
	if r.ConfigRejections, err = rejectionsValue(rejections); err != nil {
		return Run{}, err
	}
	if r.Park, err = parkValue(park); err != nil {
		return Run{}, err
	}
	if r.StandingConsent, err = optionalTimeValue(consent, "standing_consent"); err != nil {
		return Run{}, err
	}
	if r.CreatedAt, err = decodeTime(created); err != nil {
		return Run{}, err
	}
	if r.UpdatedAt, err = decodeTime(updated); err != nil {
		return Run{}, err
	}
	return r, nil
}

// encodeRejections renders the rejection lines as the JSON array the column
// stores. nil and empty both encode as [], which reads back as a resolution
// that dropped nothing; the encoding cannot say "unknown", which is the
// column's NULL and not a value.
func encodeRejections(list []string) string {
	if list == nil {
		list = []string{}
	}
	// Encoding a []string cannot fail: json.Marshal replaces invalid UTF-8
	// rather than refusing it.
	encoded, _ := json.Marshal(list)
	return string(encoded)
}

// rejectionsText converts an optional rejection list to the stored text, for
// the one insert that writes whatever the caller's Run carries.
func rejectionsText(o Optional[[]string]) Optional[string] {
	list, ok := o.Get()
	if !ok {
		return Unknown[string]()
	}
	return Known(encodeRejections(list))
}

// rejectionsValue converts stored text back to the optional list. Text that is
// present but not the stored encoding is an error rather than an unknown,
// because reporting a corrupt row as "no resolution was recorded" is the
// fabrication Optional exists to prevent.
func rejectionsValue(o Optional[string]) (Optional[[]string], error) {
	text, ok := o.Get()
	if !ok {
		return Unknown[[]string](), nil
	}
	var list []string
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		return Unknown[[]string](), fmt.Errorf("store: column config_rejections: %w", err)
	}
	return Known(list), nil
}

// parkValue converts the stored park back to the optional record. Text that is
// present but does not decode to a park Validate accepts is an error rather
// than an unknown, on rejectionsValue's terms: a corrupt row reported as "no
// bound parked this run" would read a park as a cancellation.
func parkValue(o Optional[string]) (Optional[RunPark], error) {
	text, ok := o.Get()
	if !ok {
		return Unknown[RunPark](), nil
	}
	var p RunPark
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Unknown[RunPark](), fmt.Errorf("store: column park: %w", err)
	}
	if err := p.Validate(); err != nil {
		return Unknown[RunPark](), fmt.Errorf("store: column park: %w", err)
	}
	return Known(p), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
