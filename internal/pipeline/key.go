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
	// KeyForgeRepository names the repository on the code host that the pull
	// request and checks stages act on, in the notation that host addresses a
	// repository by. It is a run input.
	//
	// It is a state key rather than something the stage seam carries because
	// it is a fact about this run and not an adapter: one service builds one
	// pipeline and serves every run, so a repository settled beside the
	// adapter would be the same repository for every run of that service.
	//
	// It is empty when the run's record does not name a repository on a host
	// this build talks to. For a repository with no code host that is
	// KeyNoCodeHost's case, and the stages that need one are passed over;
	// a stage that needs one and is reached with this empty all the same
	// refuses rather than resolving one for itself.
	KeyForgeRepository Key = "forge.repository"
	// KeyForkRepository names the fork a run pushes its branch to and proposes
	// its pull request from, in KeyForgeRepository's notation. It is a run
	// input, derived from the run's repository record the way
	// KeyForgeRepository is, and empty when that record names no fork, which
	// is a run that pushes to the upstream itself.
	//
	// The push target is decided from it, so the anchor the rebase stage
	// observes and the update the push stage makes are both on the fork's
	// branch when it names one, and the pull request stage proposes from it.
	// It is the one per-run fact that says a fork is involved: the remote the
	// isolated copy reaches the fork under is set from the same record, and
	// a run whose record names a fork that does not resolve is refused
	// before it starts rather than carried here empty.
	//
	// Its row is late, because an empty value is how every run behaved
	// before forks existed: a run that checkpointed without it resumes
	// pushing to the upstream, which is what it observed its anchor on.
	KeyForkRepository Key = "forge.fork"
	// KeyNoCodeHost says the run's repository has no code host: its upstream
	// is a path or a file:// URL, which forge.ReachLocal names. It is a run
	// input, derived from the run's repository record where
	// KeyForgeRepository is, and never from a stage failing.
	//
	// Issue #283's owner decision is what it does: every stage the stage
	// table marks as needing a code host is passed over with SkipNoCodeHost
	// as the reason, so the run pushes and then ends without a pull request
	// or checks. It is not the person's skip list, which P2 reserves for a
	// person's choice: this is a fact about the repository, read by the
	// stage node the way the empty-diff short circuit is.
	//
	// Its row is late, because false is how every run behaved before it
	// existed: a run that checkpointed without it walks the pull request and
	// checks stages as it would have.
	KeyNoCodeHost Key = "forge.none"
	// KeyBranch is the branch under validation. It is a run input.
	KeyBranch Key = "branch"
	// KeyBase is the branch target the change is rebased onto and pushed to.
	// It is a run input.
	KeyBase Key = "base"
	// KeySubmitted is the commit the push submitted to the gate, which is what
	// the run was asked to validate. It is a run input and never changes.
	KeySubmitted Key = "submitted"
	// KeySubmittedSubject is the subject of the commit KeySubmitted names: the
	// first line of its message that is not blank, as internal/vcs reads it,
	// with credentials removed by internal/redact. It is a run input and
	// never changes, and the pull request stage titles a pull request with it
	// when the run carries no intent, which is how a stage that invokes no git
	// knows what the commit says about itself.
	//
	// It is empty when the commit's message has no such line, when the
	// service could not read it, and in a run that checkpointed before this
	// key existed. Its row is late for that last reason, so such a run
	// resumes reading it as empty, which is how it behaved before, rather than
	// being refused for lacking it. It leaves the convergence bound as it was
	// in both kinds of run: no node writes it, so it cannot make two rounds of
	// one run differ, and internal/graph's Key.Late is why the empty value a
	// resumed older run reads fingerprints as the absence that run recorded.
	KeySubmittedSubject Key = "submitted.subject"
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
	// KeyApproved is the commit a completed review approved. PRD section 5 has
	// the push stage require a durable record of one this commit descends
	// from; nothing in this package checks that.
	KeyApproved Key = "approved"
	// KeyPushAnchor is where the branch the push stage forwards to stood when
	// this run observed it, before the run did its work. It is the durable
	// form of a safety.Observation, and the push stage's update is anchored to
	// it.
	//
	// It is state rather than something the push stage reads for itself
	// because of what P6 asks: an anchor read a moment before pushing always
	// holds and so protects nothing. A key is what carries the observation
	// across the stages in between and across a restart, and what makes the
	// gap between observing and deciding a real one.
	//
	// Nothing here checks when it was written or that the stage that wrote it
	// observed anything. internal/safety says the same of its own restore: the
	// provenance rests on the checkpoint the record came out of, not on the
	// type.
	KeyPushAnchor Key = "push.anchor"
	// KeyPushed is the commit the push stage forwarded.
	KeyPushed Key = "pushed"
	// KeyPullRequest identifies the pull request the run created or updated.
	KeyPullRequest Key = "pull_request"
	// KeyChecks is the checks verdict the CI stage reached.
	KeyChecks Key = "checks"
	// KeyPullRequestEnd is which state of the pull request ended the CI
	// stage's wait on its checks, when one did: PRD section 5's "Waiting on
	// checks" has the stage read the pull request before the checks on every
	// read, and a positively read merged or closed state ends the wait. The
	// value is the text of the store's closed vocabulary for what ended a
	// run, store.PullRequestEnd, and it is empty for every run whose wait
	// ended any other way.
	//
	// It is a fact the stage read and not a verdict on the run. What a run
	// so ended becomes - passed on a merge, failed on a close - is decided
	// where the run's record is written, which is internal/service's settle
	// reading this key, on the same terms as KeyCancelled; the stage body
	// writes what it read and decides nothing about the record.
	KeyPullRequestEnd Key = "pull_request.end"
	// KeyPullRequestEndHead is the commit the pull request's head stood at in
	// the read that wrote KeyPullRequestEnd, as the provider reported it. It
	// is the "head read" of PRD section 8's merge record, which the service
	// writes from these two keys and the recorded number when it settles the
	// run; it is empty whenever KeyPullRequestEnd is.
	KeyPullRequestEndHead Key = "pull_request.end_head"
	// KeyDecisionChecks is the review stage's record of what the
	// decision-model checks did over the change - the mode, the model version,
	// and how many files or traces each check covered - which the pull
	// request stage writes into the body. Its encoding belongs to
	// internal/stages, which both writes and reads it.
	KeyDecisionChecks Key = "decision.checks"
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
	// late marks a key added after runs may already have checkpointed without
	// it, which internal/graph's Key.Late defines. Only a key whose zero
	// value is how a run without it behaved may carry it.
	late bool
}

// sharedKeys is the part of the schema that is not per stage.
var sharedKeys = []keySpec{
	{KeyRepository, graph.KindText, graph.MergeNone, ownerRun, false},
	{KeyRun, graph.KindText, graph.MergeNone, ownerRun, false},
	{KeyForgeRepository, graph.KindText, graph.MergeNone, ownerRun, false},
	{KeyForkRepository, graph.KindText, graph.MergeNone, ownerRun, true},
	{KeyNoCodeHost, graph.KindBool, graph.MergeNone, ownerRun, true},
	{KeyBranch, graph.KindText, graph.MergeNone, ownerRun, false},
	{KeyBase, graph.KindText, graph.MergeNone, ownerRun, false},
	{KeySubmitted, graph.KindText, graph.MergeNone, ownerRun, false},
	{KeySubmittedSubject, graph.KindText, graph.MergeNone, ownerRun, true},
	{KeySkip, graph.KindList, graph.MergeNone, ownerRun, false},
	{KeyHead, graph.KindText, graph.MergeLastWriteWins, ownerStage, false},
	{KeyIntent, graph.KindText, graph.MergeNone, ownerStage, false},
	{KeyIntentSupplied, graph.KindBool, graph.MergeNone, ownerRun, false},
	{KeyDiffEmpty, graph.KindBool, graph.MergeNone, ownerStage, false},
	{KeyApproved, graph.KindText, graph.MergeNone, ownerStage, false},
	// The anchor carries a merge rule because the stage that observes it takes
	// fix rounds, so a run may write it more than once. Each write is a fresh
	// observation of the same target, and the last one is the state the change
	// was finally brought up to date against, which is the one the push has to
	// be anchored on.
	{KeyPushAnchor, graph.KindText, graph.MergeLastWriteWins, ownerStage, false},
	{KeyPushed, graph.KindText, graph.MergeNone, ownerStage, false},
	{KeyPullRequest, graph.KindText, graph.MergeNone, ownerStage, false},
	{KeyChecks, graph.KindText, graph.MergeNone, ownerStage, false},
	{KeyPullRequestEnd, graph.KindText, graph.MergeNone, ownerStage, false},
	{KeyPullRequestEndHead, graph.KindText, graph.MergeNone, ownerStage, false},
	// The review takes fix rounds and writes this on every execution, so it
	// carries the anchor's merge rule for the anchor's reason: the last write
	// is the record of the review the run finally stood on.
	{KeyDecisionChecks, graph.KindText, graph.MergeLastWriteWins, ownerStage, false},
	{KeyCancelled, graph.KindBool, graph.MergeNone, ownerPipeline, false},
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
// whether the round was automatic or one a person asked for at the stage's
// hold, so every stage has one.
func (s Stage) FixKey() Key { return Key("fix." + s.String()) }

// HeldKey is where the holds the last fix round of this stage raised land,
// encoded as a JSON list of findings. The fix node and the request node write
// it every round, empty when an automatic round raised none, and the stage
// node reads it on the execution that verifies that round and adds what it
// holds to the stage's report.
func (s Stage) HeldKey() Key { return Key("held." + s.String()) }

// RequestKey is where what a person asked for with a fix round at this stage's
// hold lands: the encoded FixRequest. It is the hold's graph.Halt.Detail key,
// so it belongs to that halt point alone on the same terms as AnswerKey, and
// the graph clears it whenever the run stops there.
func (s Stage) RequestKey() Key { return Key("request." + s.String()) }

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
		add(keySpec{row.stage.ReportKey(), graph.KindText, graph.MergeNone, ownerPipeline, false})
		add(keySpec{row.stage.OutcomeKey(), graph.KindText, graph.MergeLastWriteWins, ownerPipeline, false})
		add(keySpec{row.stage.AnswerKey(), graph.KindText, graph.MergeNone, ownerPipeline, false})
		// Both a stage's fix node and its request node write these, which is
		// what the merge rule is for: each write is the latest round's
		// account, whichever kind of round it was.
		add(keySpec{row.stage.FixKey(), graph.KindText, graph.MergeLastWriteWins, ownerPipeline, false})
		add(keySpec{row.stage.HeldKey(), graph.KindText, graph.MergeLastWriteWins, ownerPipeline, false})
		add(keySpec{row.stage.RequestKey(), graph.KindText, graph.MergeNone, ownerPipeline, false})
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
		out[i] = graph.Key{Name: string(key), Kind: spec.kind, Merge: spec.merge, Late: spec.late}
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
