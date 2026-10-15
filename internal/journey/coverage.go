package journey

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dayamjz/assistant/internal/principles"
)

// Reach says how far this harness drives the product for one principle. It is
// the difference between a claim about the binary that ships and a claim about
// a package inside it, and the two are not worth the same.
type Reach string

const (
	// ReachBinary is driven through the assistant binary as a process, against
	// the fixture. It is the only reach that says anything about what ships.
	ReachBinary Reach = "binary"
	// ReachPackage is driven through the package that owns the mechanism, in
	// this process, against the fixture. It is what is left where the binary
	// cannot reach a mechanism in the runs this harness drives: the intent
	// body reads the supplied intent and launches nothing, the review body
	// launches the agent this harness resolves - which the default journey
	// scripts to answer nothing, so a taken review stops at an ask recording
	// that no review happened - and the pull request body fails because the
	// run's record names no repository on the code host, so every walk here
	// asks to skip those two stages, and the test body holds wherever the
	// run's resolved configuration names no command, which is every walk but
	// the P7 branch one. The run a task's worker starts by pushing cannot ask
	// for a skip; it takes the review stage with the stand-in scripted clean
	// and holds at the first stage its configuration holds at, before the
	// pull request stage.
	ReachPackage Reach = "package"
	// ReachNone is not driven here. The reason beside it says why, and it is
	// prose nobody checks; what the row buys is that the gap is enumerable
	// rather than invisible.
	ReachNone Reach = "none"
)

// Established is one row of what this harness establishes about one principle.
type Established struct {
	// Principle is the PRD section 4 principle the row is about.
	Principle principles.Principle
	// Reach is how far the product is driven for it.
	Reach Reach
	// Note says what the journey establishes and what it does not, in the
	// terms a reader deciding whether to trust a green run needs. Nothing
	// checks it against the tests; it is written to be read.
	Note string
}

// Coverage is what this harness says about each of the PRD's principles, one
// row per principle.
//
// It is a declaration, not a measurement, and the distinction is the same one
// internal/principles draws about a citation: a row saying a principle is
// driven through the binary says a test here claims to drive it, never that
// the test is enough. What the table is for is that a principle this harness
// does not reach is a line somebody had to write rather than an absence
// nobody can see.
func Coverage() []Established {
	return []Established{
		{principles.P1, ReachBinary,
			"P1 has two halves and both are driven, so a skip takes down one of them rather than the " +
				"principle. The half that says what a push to origin must not become: an ordinary push " +
				"to origin after the binary has created a gate is driven as a process and read back off " +
				"the remote, and what is checked is that origin's own configuration is untouched, that " +
				"the push lands, and that no run exists afterwards. It reaches no method internal/ipc " +
				"restricts, so no platform refuses it for want of peer credentials, but it does need a " +
				"service, so on a platform with no local socket transport to serve this protocol over it " +
				"is skipped rather than passing. The half that says what a push to the gate by name does: " +
				"such a push is driven and the run it authorized is held to the branch and the commit " +
				"that were pushed, rather than to some run having started. That push crosses the gate's " +
				"admission hook, which asks methods internal/ipc restricts, so on a platform where that " +
				"package reads no local socket peer credentials it is skipped rather than passing. Where " +
				"either skip is taken nothing about that half is established there, and where both are, " +
				"nothing about P1 is."},
		{principles.P2, ReachBinary,
			"One run of the binary reaches every stage it does not ask to skip; a second run skips one " +
				"more of them for that run only; and a home whose configuration document asks for a " +
				"standing skip stops the service before it serves. Both runs skip review and the pull " +
				"request stage, because the review body launches the agent this harness scripts to " +
				"answer nothing and the pull request body fails because the run's record names no " +
				"repository on the code host, so no run here both takes the pull request stage and reaches the end of " +
				"the gate, and none reviews anything. Two narrow facts are what establish the third: " +
				"internal/config's key table admits no key named skip, and a key it does not admit is " +
				"refused where the document is walked, before the service binds. A second key of another " +
				"name is driven beside it and held to the same answer, so the refusal is not read off one " +
				"word. Whether some row that table does carry would apply a standing skip is what a row " +
				"means rather than what it is called, and that table is its one owner; nothing here " +
				"enumerates it. The order the stages come back in is not established here and no clause " +
				"claims it: internal/pipeline's nine named fields make another order unsayable, and " +
				"internal/service renders a run's answer by iterating that order, so a clause over the " +
				"order could not fail from what a run reports. What the run half establishes instead is " +
				"that a report came back for every stage, that every stage the run did not ask to skip " +
				"ran, and that each hold carried the " +
				"outcome it was answered with. All three parts are one test, and it starts a run through a " +
				"method internal/ipc restricts, so on a platform where that package reads no local socket " +
				"peer credentials the test is skipped rather than passing and nothing about P2 is " +
				"established there."},
		{principles.P3, ReachBinary,
			"A run through the binary is walked to its end, asking to skip the pull request stage " +
				"because its body fails when the run's record names no repository on the code host, " +
				"and every hold it reaches is read: it holds " +
				"once for each stage this harness declares a run stops at - the test, document and " +
				"lint stages, whose bodies hold for the commands nobody configured here, the push " +
				"stage, whose body holds because the run carries no completed review, and the checks " +
				"stage, whose body holds because the record names no code host - every hold is relayed " +
				"with the finding that produced " +
				"it, every one of those findings reports itself as holding for a person, and none is one " +
				"a fixer may take. The planted agent output is driven at package reach as well, because " +
				"the runs that launch a reviewer answer it from a script rather than from the planted " +
				"bytes: four take the review stage with a scripted clean report - the three walks that " +
				"reach the push stage's decision and the run a task's worker starts by pushing - and one " +
				"of the three has " +
				"the push stage's review of later commits answered with a scripted ask that holds the " +
				"push, both reaching findings.ParseReviewReport; every other walk skips the review " +
				"stage, so no report an agent wrote reaches those runs. " +
				"The run half starts a run through a method internal/ipc restricts, so on a platform where " +
				"that package reads no local socket peer credentials it is skipped rather than passing, and " +
				"the package-reach half over the planted agent output is the whole of what is established " +
				"there."},
		{principles.P4, ReachPackage,
			"The binary launches a reviewer in four runs, through the production adapter, a fixer " +
				"in two, and a task's worker in one, through the same adapter's stream mode. One of " +
				"them observes that the push stage's review of later commits " +
				"carried no session, which is one invocation off the wire rather than the P4 split: " +
				"the split is that no route hands a review a fixer's session, and a walk cannot " +
				"enumerate routes, so none here cites P4 and every other walk skips the review stage. What " +
				"is driven is the production adapter over the stand-in, which is where the type split " +
				"lives and where an invocation record can be read off the wire."},
		{principles.P5, ReachBinary,
			"Driven through the binary for the push stage's review of commits a fix round wrote after " +
				"the review, which is PRD section 14's seventh decision. A run's lint stage takes an " +
				"automatic fix round after the review stage passed, the push stage asks the reviewer " +
				"about the commits between the reviewed one and the head, measured from the reviewed " +
				"commit, carrying no session although the run's record holds the fixer session it could " +
				"have continued, and holds with that review's finding, and the remote's " +
				"branch does not move. The round's commit is the configured lint command's own edit, " +
				"committed by the product's fix body: the stand-in fixer prints and cannot write a " +
				"working copy, so no commit here is an edit an agent made. Both reviews are scripted, " +
				"the first clean and the second with one ask finding, so what is established is that " +
				"the second review is asked for, framed and obeyed, not that it would find anything. " +
				"Not driven here: a later review that approves and lets the push through, a test " +
				"stage's round, a round a person asked for at a hold, and the review stage re-checking " +
				"its own fix round; internal/stages' own tests claim those against the bodies. The walk " +
				"starts a run through a method internal/ipc restricts, so on a platform where that " +
				"package reads no local socket peer credentials it is skipped rather than passing, and " +
				"nothing about P5 is established there."},
		{principles.P6, ReachBinary,
			"The service is killed at every boundary a real run reaches and the run is driven to its " +
				"end afterwards, which is P6's own stated verification criterion; the criterion is every " +
				"boundary rather than a count of them. A boundary is a hold, so the boundaries are the " +
				"stages this harness declares a run stops at, each holding on what its configuration " +
				"or its run cannot give it. The " +
				"intent stage is not among them and no kill is manufactured for it: PRD section 5 has " +
				"it never block a run, and a stage that never holds offers nothing to kill at. Review " +
				"is not among them either: the run asks to skip it, because its body launches the " +
				"agent this harness scripts to answer nothing, and a skipped stage offers no hold. The " +
				"pull request stage is not among them for the same reason: the driven run asks to " +
				"skip it because its body fails when the run's record names no repository on the " +
				"code host, so it offers no boundary either. What " +
				"the recovered decision is held to is " +
				"that it stands at the same stage and still offers what it offered, which a checkpoint " +
				"round trip can lose; that it offers something nobody was offered is not claimed, " +
				"because internal/pipeline gives every hold the same fixed rendering and internal/graph " +
				"copies it through, so no run can report one. Two runs here reach " +
				"the push stage's decision on a stand-in reviewer scripted clean. The delivery walk's " +
				"remote is one nobody advanced, and it establishes that the push stage forwards the " +
				"submitted commit and moves the remote's branch to it. The lease walk's remote is " +
				"advanced out of band, by internal/fixture's real push from a second clone, while the " +
				"run is held between the rebase stage's observation and the push stage's decision, and " +
				"it establishes that the push stage holds on a would-discard refusal whose anchor is " +
				"the commit the run observed rather than where the branch stood at the decision, that " +
				"names as discarded exactly the commit somebody else landed and states internal/safety's " +
				"remedy, and that the remote still holds that commit. What the lease walk does not " +
				"establish is held at package reach: that following the remedy makes the decision allow " +
				"a fast-forward, which no surface here drives a run through. Both walks need a " +
				"service and a run, so they are skipped rather " +
				"than passing where internal/ipc reads no peer credentials or no local socket " +
				"transport exists. The pull request and checks stages are skipped on both, because no " +
				"record here names a repository on a code host and this fixture has none for " +
				"internal/forge to address. The killed run, and the contention check beside it, are " +
				"driven through methods internal/ipc restricts, so on a platform where that package reads " +
				"no local socket peer credentials both are skipped rather than passing, and the two " +
				"package-reach observations are the whole of what is established there. The kill path " +
				"at recovery is held at package reach and not through this harness, because no walk " +
				"here kills a service while an agent it launched is running, since the kills here land at " +
				"stage boundaries: internal/agents ends a group only after verifying its " +
				"leader's recorded identity and leaves a reused identifier and an identity with no start " +
				"time alone, and internal/service's recovery leaves a reused identifier, a record with no " +
				"identity, and an active run's group running and reports each, while a service killed " +
				"with SIGKILL leaves a record the next one ends that group by and a sibling a command-line " +
				"pattern would have matched survives. Those checks run on unix; where no identity read " +
				"exists they are skipped saying nothing is established, where no local socket transport " +
				"exists the recovery checks are skipped rather than passing, and Windows establishes the " +
				"identity read and EndGroup's verified leader and nothing about recovery. After every " +
				"kill the branch is also asked assistant sync through the binary and held to " +
				"internal/custody's pipeline-holds class with nothing to carry out, which is PRD section " +
				"11's classification of a branch whose run is resumable, so that half of section 12's " +
				"criterion is driven here. What no walk here drives through the binary is the class of " +
				"a run that ended after it moved the work - recoverable, behind, and the two diverged " +
				"rows - because no walk ends a run with its work moved and pushed to a code host: " +
				"internal/custody's tests establish each class over real repositories, and " +
				"internal/service's establishes recoverable on a commit its copy holds past the last " +
				"checkpoint, the work a kill mid-stage leaves, both at package reach. That gap is " +
				"recorded here rather than closed."},
		{principles.P7, ReachBinary,
			"The git template that would choose what a gate is born with is refused by the binary, and " +
				"the environment channel is shown closed. How the initialization came out is the whole " +
				"of what those four subtests establish: the refusals refuse with the substrings their " +
				"conditions record, and the closed channels are not refused. Whether a template hook " +
				"arrived in the gate is not among it, because no clause there looks at a hook. A whole " +
				"run is driven over the branch carrying an agent harness installation: it resolves " +
				"the trusted document, executes the trusted commands.test against the branch, " +
				"converges at the planted failure rather than walking past it, and the tripwire " +
				"file is asserted quiet with that execution as what makes the absence " +
				"discriminating, so nothing the branch installed executed and nothing it named was " +
				"selected. The same test drives the suppression refusal, and reads the rejections " +
				"of the branch's own keys in process; the run's record carries its rejections and " +
				"its resolved agent now, and the in-process read predates that rather than " +
				"establishing anything about it. A run over a trusted document that " +
				"will not parse is refused through the binary before anything launches, and the " +
				"in-process reads hold that refusal's exact shape to the planted conditions. What " +
				"stays unestablished and no clause claims: whether a template's hooks arrived or " +
				"ran, since those are receive-side and the four subtests that plant one make no " +
				"push, and which agent a run resolved, which the run's record now reports and no " +
				"check here reads. The " +
				"runs go through methods internal/ipc restricts, so on a platform where that " +
				"package reads no local socket peer credentials they are skipped rather than " +
				"passing; what is established there is the template initialization, which needs no " +
				"service, and the package-reach reads of the planted documents."},
		{principles.P8, ReachBinary,
			"Two walks cite it. In the first, a task whose event log ends on an open decision and whose " +
				"attributed validation run has passed is reported by the binary as the run's state, from " +
				"the run, with the decision marked superseded and its text absent from the detail. That " +
				"task and its run are written through internal/store, since the state planted - an open " +
				"decision under a passed attributed run - is not one internal/service's task.create can " +
				"write, so what is driven there is the reading and not the writing, and that task has no " +
				"session reference, so the process tier is not reached. In the second, the task walk, " +
				"nothing is written through internal/store: a task created with task.create on the " +
				"binary's socket is read off the binary as Running from its live terminal while the newest " +
				"event in its history claims no Running of its own, and, once its worker's push started a " +
				"run and the run stopped executing, as that run's state from the run while the terminal " +
				"was still live. Its worker is the scripted stand-in under the Claude Code adapter's " +
				"stream mode, and its run is held rather than passed, so it reaches no superseded " +
				"decision. The first reaches no method internal/ipc restricts but is asked of a service, " +
				"so on a platform with no local socket transport to serve this protocol over it is " +
				"skipped rather than passing; the second creates a task and pushes through methods " +
				"internal/ipc restricts, so on a platform where that package reads no local socket peer " +
				"credentials it is skipped rather than passing, and only the first half is established " +
				"there. Neither walk opens a hold naming a task, so PRD section 6's hold tier is not " +
				"driven here: a task reading Held from such a hold, above its run, is established in " +
				"internal/taskstate's precedence tests and through internal/cli's command surface, not " +
				"through this binary."},
		{principles.P9, ReachNone,
			"Not driven here: P9 is cited in two packages, neither of them this harness. " +
				"internal/supervisor's matrix test cites it for the table only: every wake kind " +
				"crossed with evidence present and absent is held to an oracle written from the PRD's " +
				"flowchart. internal/service's loop tests cite it for the loop's half, through Open " +
				"and the real store: with a scripted terminal double, a running record nothing " +
				"advances surfaces, an unchanged live screen absorbs until the escalation window " +
				"elapses under an injected clock and an absorbed pass moves no window start, and the " +
				"terminal-ish wait surfaces once and again after a real change and not after a " +
				"redraw; and through the product's own terminal seam over the service's session " +
				"provider, a worker the service launched is absorbed on its unchanged live screen " +
				"and surfaced as terminal gone once it has exited. Evidence derivation is " +
				"internal/taskstate's. The task walk here reaches a worker and reads its task as " +
				"Running with progress off the binary, but it classifies no wake and cites nothing " +
				"for P9, and the kind for an " +
				"authenticated check result has no producer in this build, so its rows test a path " +
				"nothing walks."},
		{principles.P10, ReachNone,
			"Not driven here: no coordinator ends a turn in this build, and no verb or method runs the " +
				"guard. P10 is cited by internal/supervisor/feed's test, for the predicate fed from the " +
				"real mechanism: a lock a second process holds, a beacon the real writer wrote, an " +
				"instance file internal/home wrote and a database the read-only open reads. A stale " +
				"beacon blocks with the holder alive, a fresh one blocks with the lock free, and the " +
				"positive control allows. internal/service's wedge test cites it for the loop: a " +
				"capture that ignores its context stops the beacon, the watchdog replaces the " +
				"incarnation and the replaced one's writes are fenced, so one incarnation writes the " +
				"beacon at a time. internal/cli's host tests cite it for the hook at turn end, driven " +
				"through cli.Run with the test's own streams: the same planted facts reach the host as " +
				"an exit status and a message with no service consulted, and the host's re-entry " +
				"signal, stop_hook_active on standard input or ASSISTANT_HOST_REENTRY, allows the one " +
				"invocation it was given to and never an unsignalled one, while input it cannot read " +
				"never allows. What none of them reaches is a coordinator's host firing the hook: " +
				"host install prints and writes the line, but no test runs a host, so the input is " +
				"the shape the host documents and the push layer is exercised as a command and not " +
				"as a turn ending."},
		{principles.P11, ReachBinary,
			"The positive half is driven through the binary by the task walk: a task created with " +
				"task.create on the binary's socket is dispatched and its worker launched by the " +
				"service, in the copy the home lays out for the task, and the harness reads that copy " +
				"with git as a clone with its own object store, distinct from the primary checkout, " +
				"whose one remote is the gate; the commit made there as the worker is absent from the " +
				"primary. The worker is the scripted stand-in under the Claude Code adapter's stream " +
				"mode, so this is the program's wiring and not a real CLI, and the stand-in runs " +
				"nothing, so the second check P11 puts in the worker's own instructions is never " +
				"run. The walk creates a task and pushes through methods internal/ipc restricts, so on " +
				"a platform where that package reads no local socket peer credentials it is skipped " +
				"rather than passing and only the package-reach half below is established there. The " +
				"refusal half - a copy that is not isolated is not launched - is not driven here. " +
				"internal/tasks' own tests " +
				"establish it at package reach and only the assertion half: AssertIsolated refuses a " +
				"real observation of every shape the principle names and passes a proper clone of " +
				"either task shape, and internal/agents strips the redirecting git variables from a " +
				"worker's environment, and internal/session's Create refuses an Isolation the assertion did " +
				"not produce before it asks for a worker. The launch edge is internal/service's, at " +
				"package reach through that package's own test rather than through this harness: its " +
				"launch asserts the copy again before Create, and a copy retargeted between dispatch " +
				"and launch is refused there and not launched; its relaunch at service start asserts " +
				"the standing copy through the same function, and a copy that became the primary " +
				"checkout is refused there with no worker started. internal/tasks' promotion cites it too, at " +
				"package reach: the standing copy is asserted before and after it is shaped, a copy " +
				"pointed at the primary's .git is refused before the primary is written to, and the " +
				"refusal is recorded. A worker started through internal/agents " +
				"directly is still started where it is told, so a green P11 does not read as no worker " +
				"launches unproven."},
		{principles.P12, ReachNone,
			"No walk here reaches the removal of an isolated copy: a run here skips the stages that " +
				"would land its branch, so its copy is kept on an unproven refusal, and the one task a " +
				"walk creates neither lands nor reports, so nothing asks for its copy to go. The binary's eject removes a gate, which is a different question. What is " +
				"claimed is claimed at package reach, both halves now. The proof's refusal half: " +
				"internal/vcs's Incorporated reports a conflict and anything it cannot compute as an " +
				"error and never as false, and internal/safety's Incorporation fails closed on an " +
				"unreadable fact with a result that is no proof. The removal half: internal/tasks' " +
				"CleanUp drives PRD section 13's three cases over a real clone and the stand-in code " +
				"host the real adapter reads, refusing committed unpushed work, passing a squash merge " +
				"with the branch deleted through the content check, and refusing a provider lookup " +
				"failure; and internal/service's tests drive the same decision from its run-copy site, " +
				"reclaimCopy, where a pushed branch passes at once and an unlanded commit keeps the copy " +
				"with the refusal on the run's answer. The task-copy sites, after a merge record lands " +
				"a delivery task and after an investigation's report is stored, pass the terminal as " +
				"the service's own reader probed it through its session provider: internal/service's " +
				"test drives the landed site over a worker it launched, which keeps the copy with the " +
				"live refusal while the worker runs and removes it on the proof once the worker is " +
				"gone, and a service with no terminal seam, which Open never builds, refuses as " +
				"unobserved."},
		{principles.P13, ReachNone,
			"Not cited here, and the row stays at none by this table's own rule that a reach is a claim " +
				"a test here makes. A walk here does drive the human rendering through the binary: " +
				"assistant tasks and assistant watch without --json, over a task whose worker declared " +
				"a wait and one whose newest entry is a relaunch note, both written through " +
				"internal/store since no verb creates a task and those histories are not ones task.create writes, and every line each verb printed is held " +
				"to the forbidden set internal/machine/vocabtest derives from the owners, with the wait " +
				"relayed and the state word absent. P13 is cited at package reach: internal/machine's " +
				"vocabulary test for the rows in its table, and internal/cli's renderer test for every " +
				"renderer internal/cli enumerates as phrased, which are " + strings.Join(p13Covered, ", ") +
				", enumerating that list rather than keeping one, with the fallback a typed refusal " +
				"that prints no Go value; internal/cli's command-line tests drive init, doctor, eject " +
				"and a push through the gate's hooks the same way. The renderers it enumerates as " +
				"evidence, which are " + strings.Join(p13Evidence, ", ") + ", are the three detailed " +
				"evidence documents PRD section 9 names, the detail view, the relay of a finding that " +
				"needs a decision and the usage view, and keep exact identifiers and stage names as P13 lets them; " +
				"internal/cli's evidence test cites P13 for the phrase-table row pointing at each and " +
				"every line beside them, against counterfeits it must reject. None is enumerated as " +
				"unsettled: the plan, the last that was, speaks internal/custody's classes, refusals " +
				"and steps through rows of the phrase table now that the custody module exists. " +
				"The refusal and usage text the command surface writes on standard error, and the " +
				"next-action line after it, are held by no test and are outside the citation. All three " +
				"lists are checked against " +
				"internal/cli's enumeration, so the note cannot name a renderer that package does not " +
				"have or miss one it does. The walk needs a service, so on a platform with no local " +
				"socket transport to serve this protocol over it is skipped rather than passing; it " +
				"reaches no method internal/ipc restricts, so no platform refuses it for want of peer " +
				"credentials, and nothing here drives a run for it."},
		{principles.P14, ReachNone,
			"One owner per fact is a property of the source rather than of a run, so driving a journey " +
				"establishes nothing about it. internal/principles and internal/pipeline's schema test " +
				"claim it, and internal/store's shape transition test claims it for a task's shape, " +
				"repository and branch: one row, flipped in one write."},
	}
}

// p13Covered, p13Evidence and p13Remaining name internal/cli's human
// renderers by the names its Renderings enumeration gives them, one list per
// voice: the ones that speak through internal/machine's phrase table, the
// evidence documents, and the ones that still print this build's own words
// for want of settled wording. The P13 row's note is built from them, and
// P13Renderers hands them to the test that holds all three to that
// enumeration, so a renderer added or rerouted there fails here rather than
// leaving the note to drift.
var (
	p13Covered = []string{"status", "run", "runs", "tasks", "task", "service", "event", "host stop", "host install",
		"decision", "doctor", "lifecycle", "init", "plan", "eject", "health", "admission", "notification",
		"task answer", "catch up", "marked seen", "task created", "task message", "task stop", "task relaunch",
		"task promotion", "wakes", "wakes acknowledged", "supervision", "fallback"}
	p13Evidence  = []string{"details", "findings", "usage"}
	p13Remaining []string
)

// P13Renderers returns the renderers the P13 row says the phrase table
// covers, the ones it says are evidence documents, and the ones it says
// remain, as copies, for the test that checks all three against
// internal/cli's enumeration.
func P13Renderers() (covered, evidence, remaining []string) {
	return slices.Clone(p13Covered), slices.Clone(p13Evidence), slices.Clone(p13Remaining)
}

// PackageDir is this package's directory relative to the module root. Citations
// are reported with module-relative paths, so this is what tells a citation
// made here from one made anywhere else.
const PackageDir = "internal/journey"

// CoverageReport describes every way the table and the citations in this
// package disagree, and is empty when they agree.
//
// Both directions are checked and the second is the one that holds the line. A
// row saying a principle is driven here, with no test here claiming it, is a
// table that describes a harness somebody meant to write. A row saying a
// principle is not driven here, with a test here claiming it, is a gap
// declaration that outlived the gap, which is exactly the shape
// internal/principles refuses for the repository as a whole.
//
// Neither direction says a test checks what it claims. Nothing can: a citation
// is a claim a test makes about itself.
func CoverageReport(root string) (string, error) {
	cited, err := citedHere(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	seen := map[principles.Principle]bool{}
	for _, row := range Coverage() {
		if seen[row.Principle] {
			fmt.Fprintf(&b, "%s: the table has more than one row for it, and every fact has one owner.\n", row.Principle)
		}
		seen[row.Principle] = true
		if strings.TrimSpace(row.Note) == "" {
			fmt.Fprintf(&b, "%s: the row says nothing about what is or is not established.\n", row.Principle)
		}
		claims := cited[row.Principle]
		switch {
		case row.Reach == ReachNone && len(claims) > 0:
			fmt.Fprintf(&b, "%s: the table says this harness does not reach it, and %s here cites it. "+
				"Change the row or drop the citation.\n", row.Principle, strings.Join(claims, ", "))
		case row.Reach != ReachNone && len(claims) == 0:
			fmt.Fprintf(&b, "%s: the table says this harness drives it at %s reach, and no test here cites it. "+
				"Add principles.Cite to the test that drives it, or change the row.\n", row.Principle, row.Reach)
		}
	}
	for _, listed := range principles.All() {
		if !seen[listed] {
			fmt.Fprintf(&b, "%s: this package has a constant for it and the table has no row. "+
				"Every principle is either driven here or declared not to be.\n", listed)
		}
	}
	for principle, claims := range cited {
		if !seen[principle] {
			fmt.Fprintf(&b, "%s: cited by %s here and the table has no row for it.\n",
				principle, strings.Join(claims, ", "))
		}
	}
	return b.String(), nil
}

// citedHere is every principle a test in this package claims, by the tests
// that claim it.
func citedHere(root string) (map[principles.Principle][]string, error) {
	all, err := principles.Citations(root)
	if err != nil {
		return nil, err
	}
	here := map[principles.Principle][]string{}
	for _, citation := range all {
		if filepath.ToSlash(filepath.Dir(citation.File)) != PackageDir {
			continue
		}
		name := citation.Test
		if !slices.Contains(here[citation.Principle], name) {
			here[citation.Principle] = append(here[citation.Principle], name)
		}
	}
	for principle := range here {
		slices.Sort(here[principle])
	}
	return here, nil
}
