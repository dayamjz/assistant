package taskstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/store"
)

// ErrUnreadable means a stored state record could not be decoded into this
// package's vocabulary. It is a refusal: the reader does not substitute the
// row's raw state for an answer it cannot interpret.
var ErrUnreadable = errors.New("taskstate: the stored state cannot be read")

// Current is the stored answer for one task, decoded.
type Current struct {
	Resolution
	// Revision and ResolvedAt are the record's, which count and date changes
	// of the answer.
	Revision   int64
	ResolvedAt time.Time
	// Unrecorded is true for a row written before the resolver existed: its
	// Superseded and Basis are empty because nobody recorded them, which is a
	// different fact from an answer that outranked nothing.
	Unrecorded bool
	// Row is the record as the store holds it, for a surface that reports the
	// record itself.
	Row store.TaskState
}

// Reader reads settled state. It never computes an answer and never writes.
type Reader struct {
	store *store.Store
	probe Prober
}

// NewReader builds a Reader. The probe is used by Audit only, and must be the
// one the Settler was built with for Audit's verdict to compare like with like.
func NewReader(s *store.Store, probe Prober) *Reader { return &Reader{store: s, probe: probe} }

// Current returns the stored answer for a task, or store.ErrNotFound. A stored
// record this package cannot interpret is ErrUnreadable.
func (r *Reader) Current(ctx context.Context, taskID string) (Current, error) {
	st, err := r.store.TaskState(ctx, taskID)
	if err != nil {
		return Current{}, err
	}
	return decode(st)
}

// All returns the stored answer for every task, newest task first.
func (r *Reader) All(ctx context.Context) ([]Current, error) {
	tasks, err := r.store.Tasks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Current, 0, len(tasks))
	for _, t := range tasks {
		c, err := r.Current(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// DriftError is the typed result of an Audit that found the stored answer
// differing from what the inputs say now.
type DriftError struct {
	TaskID string
	// Stored is what the record says and Resolved is what the inputs say.
	Stored, Resolved Resolution
}

func (e *DriftError) Error() string {
	why := fmt.Sprintf("the record says %s from %s, the inputs say %s from %s",
		e.Stored.State, e.Stored.Source, e.Resolved.State, e.Resolved.Source)
	if e.Resolved.Basis.RunID != "" {
		why += fmt.Sprintf(" (run %s is %s)", e.Resolved.Basis.RunID, e.Resolved.Basis.RunStatus)
	}
	return fmt.Sprintf("taskstate: the state of task %s is stale: %s", e.TaskID, why)
}

// Audit re-resolves a task in memory, writes nothing, and returns a
// *DriftError when the stored answer is not what its inputs resolve to. It is
// the detector for a writer that changed an input and did not settle; it
// cannot say how long the record was stale, and a probe that disagrees with
// the Settler's will report drift that is not there.
func (r *Reader) Audit(ctx context.Context, taskID string) error {
	snap, err := r.store.TaskSnapshot(ctx, taskID)
	if err != nil {
		return err
	}
	stored, ok := snap.Stored.Get()
	if !ok {
		return fmt.Errorf("%w: task %s has no state record", store.ErrNotFound, taskID)
	}
	current, err := decode(stored)
	if err != nil {
		return err
	}
	resolved := Resolve(inputsOf(snap, probeOf(ctx, r.probe, snap.Task)))
	if current.Unrecorded || !current.sameAnswer(resolved) {
		return &DriftError{TaskID: taskID, Stored: current.Resolution, Resolved: resolved}
	}
	return nil
}

func decode(st store.TaskState) (Current, error) {
	c := Current{Revision: st.Revision, ResolvedAt: st.ResolvedAt, Row: st}
	c.TaskID, c.State, c.Source, c.Detail = st.TaskID, State(st.State), Source(st.Source), st.Detail
	if !c.State.Recognized() {
		return Current{}, fmt.Errorf("%w: task %s has state %q", ErrUnreadable, st.TaskID, st.State)
	}
	if !c.Source.Recognized() {
		return Current{}, fmt.Errorf("%w: task %s has source %q", ErrUnreadable, st.TaskID, st.Source)
	}
	sup, supKnown := st.Superseded.Get()
	basis, basisKnown := st.Basis.Get()
	if !supKnown || !basisKnown {
		c.Unrecorded = true
		return c, nil
	}
	if err := strictJSON(sup, &c.Superseded); err != nil {
		return Current{}, fmt.Errorf("%w: task %s superseded: %w", ErrUnreadable, st.TaskID, err)
	}
	for _, s := range c.Superseded {
		if !s.Claimed.Recognized() || !s.By.Recognized() {
			return Current{}, fmt.Errorf("%w: task %s superseded an event with claimed state %q by %q",
				ErrUnreadable, st.TaskID, s.Claimed, s.By)
		}
	}
	if len(c.Superseded) == 0 {
		c.Superseded = nil
	}
	if err := strictJSON(basis, &c.Basis); err != nil {
		return Current{}, fmt.Errorf("%w: task %s basis: %w", ErrUnreadable, st.TaskID, err)
	}
	return c, nil
}

// strictJSON decodes text into v and refuses unknown fields and trailing data.
func strictJSON(text string, v any) error {
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data")
	}
	return nil
}
