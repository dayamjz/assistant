package pipeline

import (
	"fmt"
	"sort"

	"github.com/dayamjz/assistant/internal/graph"
)

// Key is a pipeline state key. The set is closed: the table in this file is
// the whole schema, and a stage that reads or writes a key the table does not
// hold fails to build. Every fact about a key - the kind of value it holds,
// how writes to it combine, and who may write it - is one row, per P14.
type Key string

// The keys a run carries across the whole pipeline. A stage names the ones it
// reads and writes; the per-stage keys below belong to this package.
const (
	// KeyRepository identifies the repository the run validates, as
	// internal/store records it. It is a run input.
	//
	// A stage body needs it to locate the isolated copy the run works in.
	// That copy's path is derived from this and KeyRun rather than carried
	// here, per PRD section 8's ordering rule: the run's row is written
	// before its directory so a directory with no row is safe to remove, and
	// a durable second copy of the path would give that rule a second fact to
	// stay consistent with.
	KeyRepository Key = "repository"
	// KeyRun identifies the run, as internal/store records it. It is a run
	// input.
	//
	// It is here because a stage body cannot reach it otherwise: graph.Body
	// is handed a reader and a writer, and the run identifier stops at
	// Executor.Run. Carrying it as state rather than widening the execution
	// engine keeps internal/graph free of anything but execution, and it is
	// the same shape as KeyBranch and KeySubmitted, which also restate a fact
	// the run's store row holds.
	KeyRun Key = "run"
	// KeyBranch is the branch under validation. It is a run input.
	KeyBranch Key = "branch"
	// KeyBase is the branch target the change is rebased onto and pushed to.
	// It is a run input.
	KeyBase Key = "base"
	// KeySubmitted is the commit the push submitted to the gate, which is what
	// the run was asked to validate. It is a run input and never changes.
	KeySubmitted Key = "submitted"
	// KeyHead is the commit the run is working on now. It starts at
	// KeySubmitted and moves when a rebase or a fix round rewrites it, so it
	// carries a merge rule, which is what lets more than one node write it.
	KeyHead Key = "head"
	// KeyIntent is what the change set out to do, in words.
	KeyIntent Key = "intent"
	// KeyIntentSupplied says whether the intent was supplied as authoritative
	// acceptance criteria. An intent that was not is a low-confidence hint,
	// and the two are framed differently downstream.
	//
	// It is a run input. The bit asserts that a person supplied the criteria,
	// which no stage can make true, so no stage may write it. A stage may
	// still write intent text into KeyIntent; what it may not do is promote
	// the text it recorded to authoritative.
	KeyIntentSupplied Key = "intent.supplied"
	// KeyDiffEmpty says that nothing remains to change. Every stage node reads
	// it and does not run its body while it holds, which is PRD section 5's
	// empty-diff short circuit; the rebase stage is what sets it.
	KeyDiffEmpty Key = "diff.empty"
	// KeyObservation is where the run's branch target stood when the run
	// actually looked at it, as the durable record internal/safety's
	// RestoreObservedFromCheckpoint reads back, encoded as JSON.
	//
	// It exists because PRD principle P6's anchor has to be taken before the
	// run does its work and used after: the rebase stage observes, and the
	// push stage decides on what the rebase stage saw. A push stage that read
	// the target again would be anchoring on the tip it just read, which is
	// the trap P6 names outright.
	//
	// It is state rather than a value handed from one stage to the next
	// because there is nothing to hand it along: a graph.Checkpoint's one
	// stage-writable field is its state, so state is what survives the
	// restart PRD section 13 requires a run to survive between stage 2 and
	// stage 7. Nothing here reads or validates the record; internal/safety
	// owns what it means.
	KeyObservation Key = "observation"
	// KeyApproved is the commit a completed review approved. PRD section 5 has
	// the push stage require a durable record of one this commit descends
	// from; nothing in this package checks that.
	KeyApproved Key = "approved"
	// KeyPushed is the commit the push stage forwarded.
	KeyPushed Key = "pushed"
	// KeyPullRequest identifies the pull request the run created or updated.
	KeyPullRequest Key = "pull_request"
	// KeyChecks is the checks verdict the CI stage reached.
	KeyChecks Key = "checks"
	// KeySkip names the stages this one run skips. It is a run input, per P2:
	// a person may skip a stage on purpose for one run, and no standing
	// configuration may skip one on their behalf, which is why nothing in
	// Options can write it.
	KeySkip Key = "skip"
	// KeyCancelled says a person cancelled the run at a hold. This package's
	// own cancel node writes it.
	KeyCancelled Key = "cancelled"
)

// owner says who may write a key, which is the part of a key's row that this
// package enforces rather than describes.
type owner uint8

const (
	// ownerRun marks a key set once in a run's initial state and written by no
	// node at all.
	ownerRun owner = iota
	// ownerStage marks a key a stage implementation or the fixer may declare a
	// write of.
	ownerStage
	// ownerPipeline marks a key this package's own nodes write. A stage that
	// declares a write of one fails to build, because it would give a fact
	// this package reports a second author.
	ownerPipeline
)

// refusal says why a key with this owner is not one a stage may write. The two
// reasons are opposite, so a stage author told the wrong one would look for the
// wrong mistake.
func (o owner) refusal() string {
	if o == ownerRun {
		return "is a run input no node writes"
	}
	return "the pipeline's own nodes write"
}

// keySpec is one row of the state schema.
type keySpec struct {
	key   Key
	kind  graph.Kind
	merge graph.Merge
	owner owner
}

// sharedKeys is the part of the schema that is not per stage.
var sharedKeys = []keySpec{
	{KeyRepository, graph.KindText, graph.MergeNone, ownerRun},
	{KeyRun, graph.KindText, graph.MergeNone, ownerRun},
	{KeyBranch, graph.KindText, graph.MergeNone, ownerRun},
	{KeyBase, graph.KindText, graph.MergeNone, ownerRun},
	{KeySubmitted, graph.KindText, graph.MergeNone, ownerRun},
	{KeySkip, graph.KindList, graph.MergeNone, ownerRun},
	{KeyHead, graph.KindText, graph.MergeLastWriteWins, ownerStage},
	{KeyIntent, graph.KindText, graph.MergeNone, ownerStage},
	{KeyIntentSupplied, graph.KindBool, graph.MergeNone, ownerRun},
	{KeyDiffEmpty, graph.KindBool, graph.MergeNone, ownerStage},
	{KeyObservation, graph.KindText, graph.MergeNone, ownerStage},
	{KeyApproved, graph.KindText, graph.MergeNone, ownerStage},
	{KeyPushed, graph.KindText, graph.MergeNone, ownerStage},
	{KeyPullRequest, graph.KindText, graph.MergeNone, ownerStage},
	{KeyChecks, graph.KindText, graph.MergeNone, ownerStage},
	{KeyCancelled, graph.KindBool, graph.MergeNone, ownerPipeline},
}

// ReportKey is where the stage's report lands, encoded as JSON. The stage node
// writes it and the fix node reads it; a stage may read any of them, which is
// how the pull request stage narrates what every stage found.
func (s Stage) ReportKey() Key { return Key("report." + s.String()) }

// OutcomeKey is where the stage's outcome lands. The stage node writes it and,
// for a stage a person held, so does the hold node, which is why it carries a
// merge rule.
func (s Stage) OutcomeKey() Key { return Key("outcome." + s.String()) }

// AnswerKey is where the answer to this stage's hold lands. It belongs to that
// halt point alone: the graph refuses any other writer and refuses a run that
// starts with one already set.
func (s Stage) AnswerKey() Key { return Key("answer." + s.String()) }

// FixKey is where the fixer's sanitized summary of the last round lands. It
// carries that summary from one fix round to the next round of the same stage,
// and it exists only for the stages that take automatic fix rounds.
func (s Stage) FixKey() Key { return Key("fix." + s.String()) }

// schema is every key the pipeline declares, built once from the shared table
// and the nine stages. It is the one place a key's declaration is looked up.
var schema = buildSchema()

func buildSchema() map[Key]keySpec {
	out := make(map[Key]keySpec, len(sharedKeys)+4*len(stageTable))
	add := func(spec keySpec) {
		if _, dup := out[spec.key]; dup {
			// The tables are this package's own and are checked by the schema
			// test; a duplicate here is a defect in them, not input.
			panic("pipeline: state key declared twice: " + string(spec.key))
		}
		out[spec.key] = spec
	}
	for _, spec := range sharedKeys {
		add(spec)
	}
	for _, row := range stageTable {
		add(keySpec{row.stage.ReportKey(), graph.KindText, graph.MergeNone, ownerPipeline})
		add(keySpec{row.stage.OutcomeKey(), graph.KindText, graph.MergeLastWriteWins, ownerPipeline})
		add(keySpec{row.stage.AnswerKey(), graph.KindText, graph.MergeNone, ownerPipeline})
		if row.rounds != nil {
			add(keySpec{row.stage.FixKey(), graph.KindText, graph.MergeNone, ownerPipeline})
		}
	}
	return out
}

// Keys returns every state key the pipeline declares, sorted. The set does not
// depend on configuration: a run's state holds the same keys whatever the fix
// round limits are.
func Keys() []Key {
	out := make([]Key, 0, len(schema))
	for key := range schema {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// graphKeys returns the schema as the graph's key declarations.
func graphKeys() []graph.Key {
	keys := Keys()
	out := make([]graph.Key, len(keys))
	for i, key := range keys {
		spec := schema[key]
		out[i] = graph.Key{Name: string(key), Kind: spec.kind, Merge: spec.merge}
	}
	return out
}

// declaration says which of the three declarations is being checked, because
// the schema admits a different set to each. A fixer's writes are the narrowest
// of the three.
type declaration uint8

const (
	// declaredReads is a stage's or a fixer's reads.
	declaredReads declaration = iota
	// declaredStageWrites is one stage implementation's writes.
	declaredStageWrites
	// declaredFixerWrites is the pipeline's one fixer's writes.
	declaredFixerWrites
)

// checkDeclared refuses a read or write declaration this schema does not
// admit. what names the declaration in the refusal.
func checkDeclared(who, what string, keys []Key, kind declaration) error {
	seen := make(map[Key]struct{}, len(keys))
	for _, key := range keys {
		spec, ok := schema[key]
		if !ok {
			return fmt.Errorf("%w: %s %s state key %q", ErrUndeclaredKey, who, what, key)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: %s %s state key %q more than once", ErrUndeclaredKey, who, what, key)
		}
		seen[key] = struct{}{}
		if kind == declaredReads {
			continue
		}
		if spec.owner != ownerStage {
			return fmt.Errorf("%w: %s writes state key %q, which %s", ErrReservedKey, who, key, spec.owner.refusal())
		}
		if kind == declaredFixerWrites && spec.merge == graph.MergeNone {
			return fmt.Errorf("%w: %s writes state key %q, which declares no merge rule",
				ErrUnmergeableFixerWrite, who, key)
		}
	}
	return nil
}
