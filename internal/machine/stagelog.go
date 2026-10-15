package machine

import "time"

// Activity is what the segment advancing a run is doing at the moment an
// answer was built: which stage its position belongs to, where that stage
// stands in its fix loop, and how long the node at the run's position has
// been running.
//
// It composes its owners rather than restating them. The stage is the run's
// position read through internal/pipeline; the round and its limit are
// pipeline.RoundOf over the run's own counters, which internal/graph keeps
// as the one record of how often each edge was taken; and Since is when
// internal/store appended the checkpoint naming that position, which a
// running run writes immediately before the node it names starts. None of
// the three is kept anywhere for this answer.
//
// It describes a node in flight and nothing else, so an answer carries it
// only when the service holding the run found a segment advancing it. It is
// not evidence of progress: a node can run a long time and still be working,
// and nothing reads Active to decide anything about the run.
type Activity struct {
	// Stage is the stage the run's position belongs to: its own check, its
	// hold being answered, or one of its fix rounds.
	Stage string `json:"stage"`
	// Round is how many fix rounds the stage has entered, automatic and
	// requested together: while the stage's check runs, the rounds before
	// it, so zero is the first look; while a round runs, that round.
	Round int `json:"round"`
	// Limit is the automatic round limit the run is held to at this stage,
	// zero for a stage that takes no automatic round in this run.
	Limit int `json:"limit"`
	// Since is when the node at the run's position began, to within the
	// checkpoint write that preceded it.
	Since time.Time `json:"since"`
	// Active is how long that node had been running when the answer was
	// built.
	Active time.Duration `json:"active_ns"`
}

// StageLogTailBytes bounds how much of a per-stage log one read returns. PRD
// section 8 makes the log the authoritative full output and has anything
// travelling elsewhere be a bounded projection that says what it left out and
// how to read the rest, so a read returns at most this many bytes from the
// end of the file and StageLog says how many it omitted and where the whole
// of it is. The bound is the program's and not a setting.
const StageLogTailBytes = 64 << 10

// StageLog is the answer to a read of one stage's log in one run: the end of
// PRD section 8's per-stage log, bounded by StageLogTailBytes.
//
// A log is evidence and never state, per P8: it is what a stage and its
// agent calls wrote while they ran, appended and never revised, and nothing
// that decides anything about a run reads it. A run's state is its record and
// its checkpoint.
type StageLog struct {
	// Run is the run's identifier.
	Run string `json:"run"`
	// Stage is the stage's name. A fix round is logged under the stage whose
	// findings it fixes.
	Stage string `json:"stage"`
	// Path is where the whole log is, which is how to read what Omitted
	// counts.
	Path string `json:"path"`
	// Written is whether the log exists. A stage that never ran a body or a
	// round, in a run this build walked, has none.
	Written bool `json:"written"`
	// Size is the log's length in bytes when it was read.
	Size int64 `json:"size"`
	// Omitted is how many bytes at the start of the log Text leaves out.
	Omitted int64 `json:"omitted"`
	// Text is the end of the log, starting at a line boundary, and passed
	// through internal/redact once more on the way out.
	Text string `json:"text"`
}
