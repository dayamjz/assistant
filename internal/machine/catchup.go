package machine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// CatchUp is PRD section 9's catching up: the one read assistant watch
// starts with, answering what is waiting on the person, what landed since
// they last marked what they had seen, what is under way, and what stopped
// without asking them anything. The person's rendering and the machine
// interface are this one shape (P14), and every part composes the records
// that own its facts rather than restating them: a hold is a store.Hold, a
// run a Run, a task a Task with the state internal/taskstate resolved, and a
// landed record the store.MergeRecord or store.TaskCleanup itself.
//
// Only Landed is filtered by the seen mark, and it is bounded by
// LandedLimit with MoreLanded counting the rest, which stay unseen rather
// than being dropped. Waiting, UnderWay and Stopped are never filtered:
// an open decision is shown until it is answered, and the other two are
// what is true now.
//
// The read's digest, its four parts in counts, is not a field: it is
// Digest, computed from the parts on every call, and MarshalJSON writes it
// beside them, so the counts a structured answer carries and the parts they
// count cannot come apart and no surface has an assignment site for one.
type CatchUp struct {
	// Waiting is every open decision, a task's hold and a run's, oldest
	// first, each with the answer that closes it.
	Waiting []Waiting `json:"waiting"`
	// Landed is the landed records not yet marked seen, newest first, at
	// most LandedLimit of them.
	Landed []Landed `json:"landed"`
	// MoreLanded is how many more unseen landed records there are than
	// Landed shows.
	MoreLanded int `json:"more_landed"`
	// UnderWay is the work in flight and the runs being advanced.
	UnderWay UnderWay `json:"under_way"`
	// Stopped is every task that resolves Failed or Unknown, each with its
	// next action in its phrase.
	Stopped []Task `json:"stopped"`
	// Supervision is the pull guard's finding over the same fleet,
	// computed by the service that answered, as Tasks carries it: the
	// fleet view warned before this read existed, and the read replacing
	// its opening listing keeps the warning rather than dropping it.
	Supervision *Supervision `json:"supervision,omitempty"`
}

// Digest is the catch-up read in counts, the fleet digest assistant watch
// prints ahead of the four parts. Every count is of this read's own parts
// and of nothing else, so it adds no fact the parts do not carry and counts
// nothing a person marked seen: Landed is what the seen mark left unseen,
// shown or counted in MoreLanded.
//
// Unconfirmed is the one count that is not a part's length. It is how many
// of the tasks under way resolve Running without Resolution.Progress: work
// whose worker's own newest word says it is on the work while no live
// terminal was observed on this read, which internal/taskstate resolves
// from the worker's history rather than from the process tier. The digest
// says that work was not seen moving rather than counting it as moving, and
// TaskPhrase gives the task's own line the same reading.
//
// A task resolved Unknown is in both UnderWay and Stopped, because the read
// lists it in both parts (internal/service's catchup.go says why), so the
// counts are not a partition of the fleet and are not to be summed.
func (c CatchUp) Digest() Digest {
	d := Digest{
		Waiting:   len(c.Waiting),
		Landed:    len(c.Landed) + c.MoreLanded,
		UnderWay:  len(c.UnderWay.Tasks),
		Advancing: len(c.UnderWay.Runs),
		Stopped:   len(c.Stopped),
	}
	for _, task := range c.UnderWay.Tasks {
		if unconfirmed(task) {
			d.Unconfirmed++
		}
	}
	return d
}

// unconfirmed reports whether a task resolves Running with no positive
// evidence behind it. internal/taskstate sets Progress for Running only when
// the terminal was observed live, so a false one is the worker's own word
// and nothing more.
func unconfirmed(t Task) bool {
	return t.State.State == taskstate.Running && !t.State.Progress
}

// MarshalJSON writes the read with its digest beside the parts, under the
// key digest. The digest is computed here from the parts rather than
// carried, so what travels is the parts and their counts as one reading,
// and a decoded read recomputes the same counts through Digest. HTML
// escaping is off for the reason Run.MarshalJSON gives.
func (c CatchUp) MarshalJSON() ([]byte, error) {
	type fields CatchUp
	var document bytes.Buffer
	enc := json.NewEncoder(&document)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		fields
		Digest Digest `json:"digest"`
	}{fields(c), c.Digest()}); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(document.Bytes(), []byte("\n")), nil
}

// UnmarshalJSON reads a read back, digest included. The digest is not kept,
// because Digest recomputes it from the parts, and a document whose digest
// disagrees with its own parts is refused rather than read: MarshalJSON
// cannot produce one, so such a document was not written by this shape. A
// document with no digest at all decodes, since only the parts are facts.
func (c *CatchUp) UnmarshalJSON(data []byte) error {
	type fields CatchUp
	var read struct {
		fields
		Digest *Digest `json:"digest"`
	}
	if err := json.Unmarshal(data, &read); err != nil {
		return err
	}
	decoded := CatchUp(read.fields)
	if read.Digest != nil && *read.Digest != decoded.Digest() {
		return fmt.Errorf("the catch-up read carries the digest %+v and its parts count %+v", *read.Digest, decoded.Digest())
	}
	*c = decoded
	return nil
}

// Digest is the catch-up read's four parts in counts, as CatchUp.Digest
// computes it. It is a reading of one CatchUp and owns no fact of its own.
type Digest struct {
	// Waiting is how many decisions wait on the person: len(Waiting).
	Waiting int `json:"waiting"`
	// Landed is how many landed records are not yet marked seen: the ones
	// Landed shows and the ones MoreLanded counts.
	Landed int `json:"landed"`
	// UnderWay is how many tasks are in flight: len(UnderWay.Tasks).
	UnderWay int `json:"under_way"`
	// Unconfirmed is how many of UnderWay resolve Running with no live
	// terminal observed, which is never counted as work seen moving.
	Unconfirmed int `json:"unconfirmed"`
	// Advancing is how many runs a segment is advancing now:
	// len(UnderWay.Runs).
	Advancing int `json:"advancing"`
	// Stopped is how many tasks stopped without a decision: len(Stopped).
	Stopped int `json:"stopped"`
}

// Shown is the landed records this read showed, in the order it showed
// them, which is what a mark names back through MarkSeenRequest. It is read
// off Landed and nothing else, so the records counted in MoreLanded and any
// recorded after the read are not in it.
func (c CatchUp) Shown() []store.SeenRecord {
	out := make([]store.SeenRecord, 0, len(c.Landed))
	for _, l := range c.Landed {
		out = append(out, l.Record())
	}
	return out
}

// Waiting is one decision waiting on the person: an open hold record, or a
// run held at a decision its checkpoint records. Exactly one of Hold and Run
// is set. A hold naming a task carries the task as it stands, so a person
// reads which work it holds.
type Waiting struct {
	// Hold is the open hold record, as internal/store holds it.
	Hold *store.Hold `json:"hold,omitempty"`
	// Task is the task the hold names, when it names one.
	Task *Task `json:"task,omitempty"`
	// Run is the run held at a decision, with the decision and its options.
	Run *Run `json:"run,omitempty"`
	// Answer is how the decision is answered, through the existing answer
	// path; there is no second way to resolve a hold.
	Answer Answering `json:"answer"`
}

// Answering is the command line that closes one listed decision: assistant
// followed by Args and then the answer. It is the existing answer path and
// nothing new: --answer on the command with no verb, given --task and the
// task for a hold a task raised with no run behind it, and run from the
// change's checkout for a run, because there --answer answers the run of
// the branch it is run on.
type Answering struct {
	// Args are the arguments after the program name, ending with --answer;
	// the answer itself follows them.
	Args []string `json:"args"`
	// WorkingPath is the checkout a run's answer is given from, on the
	// run's branch, and empty for a task's.
	WorkingPath string `json:"working_path,omitempty"`
	// Branch is the branch a run's answer is given from, and empty for a
	// task's.
	Branch string `json:"branch,omitempty"`
}

// TaskAnswering is how a hold naming a task and no run is answered.
func TaskAnswering(task string) Answering {
	return Answering{Args: []string{"--task", task, "--answer"}}
}

// DomainAnswering is how a delegated domain's decision is answered, the
// hold its relaunch bound raised: --domain naming the domain, then
// --answer.
func DomainAnswering(domain string) Answering {
	return Answering{Args: []string{"--domain", domain, "--answer"}}
}

// RunAnswering is how a run's decision is answered: from the run's
// checkout, on its branch.
func RunAnswering(workingPath, branch string) Answering {
	return Answering{Args: []string{"--answer"}, WorkingPath: workingPath, Branch: branch}
}

// Landed is one landed record: a merge record that reads merged, of the run
// a task's Landed is derived from, or a delivery task's cleanup record with
// its removal recorded, which its Complete is derived from. These are the
// records PRD section 6's terminal tier reads. Exactly one of Merge and
// Cleanup is set, and Task is the task it landed, as it stands.
type Landed struct {
	// Task is the task the record landed.
	Task Task `json:"task"`
	// Merge is the merge record, for a merge.
	Merge *store.MergeRecord `json:"merge,omitempty"`
	// Cleanup is the cleanup record, for a copy removed after the work
	// landed.
	Cleanup *store.TaskCleanup `json:"cleanup,omitempty"`
}

// Record names the landed record the way that record is keyed, which is
// what the seen mark holds: a merge record by its run, a cleanup record by
// its task.
func (l Landed) Record() store.SeenRecord {
	if l.Merge != nil {
		return store.SeenRecord{Kind: store.SeenMerge, Key: l.Merge.Run}
	}
	if l.Cleanup != nil {
		return store.SeenRecord{Kind: store.SeenCleanup, Key: l.Cleanup.TaskID}
	}
	return store.SeenRecord{}
}

// At is when the record says it landed: the merge record's own time, or
// the cleanup record's removal. It orders a read and decides nothing about
// whether the record was seen, which only the seen mark does.
func (l Landed) At() time.Time {
	if l.Merge != nil {
		return l.Merge.RecordedAt
	}
	if l.Cleanup != nil {
		if at, ok := l.Cleanup.RemovedAt.Get(); ok {
			return at
		}
		return l.Cleanup.RecordedAt
	}
	return time.Time{}
}

// LandedLimit is how many unseen landed records one read shows. It is a
// fixed count in the program and not a setting, which PRD section 9 says in
// as many words; the rest are counted and stay unseen.
const LandedLimit = 10

// ShowLanded orders unseen landed records newest first, the record's key
// breaking a tie so the order does not depend on the order they were read
// in, and keeps LandedLimit of them, returning how many it left out. It
// reads only what it is handed: which records are unseen is the caller's
// read of the seen mark.
func ShowLanded(unseen []Landed) (shown []Landed, more int) {
	out := append([]Landed(nil), unseen...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].At(), out[j].At()
		if !a.Equal(b) {
			return a.After(b)
		}
		ra, rb := out[i].Record(), out[j].Record()
		if ra.Kind != rb.Kind {
			return ra.Kind < rb.Kind
		}
		return ra.Key < rb.Key
	})
	if len(out) > LandedLimit {
		return out[:LandedLimit], len(out) - LandedLimit
	}
	return out, 0
}

// UnderWay is what is moving now: every task PRD section 6's work-in-flight
// definition counts, and every run a segment is advancing, each with its
// resolved state.
type UnderWay struct {
	// Tasks are the tasks in flight, newest first.
	Tasks []Task `json:"tasks"`
	// Runs are the runs being advanced, as their answers stand.
	Runs []Run `json:"runs"`
}

// Marked is the answer to a mark: the rows of the seen mark the request
// named, as they stand. A record marked before keeps its first row.
type Marked struct {
	// Marks are the seen mark's rows for the named records, in the order
	// they were named.
	Marks []store.SeenMark `json:"marks"`
}
