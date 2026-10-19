package config

import "errors"

// Policy is what one delivery posture means, in the three terms every mode
// states: what has to have run before the change is delivered, what the
// program may publish, and who may authorize the change landing. It is a
// statement about the mechanism and not a second switch: nothing reads a
// Policy to decide what a run or a task does, because internal/service
// decides a task's path from the posture recorded on the task. The residual
// gap is that direction: a change to that mechanism that falsified a row
// fails nothing here, so a row is only as true as the mechanism it names.
type Policy struct {
	// Delivery is the posture this row states.
	Delivery Delivery `json:"delivery"`
	// Validation is what has to have run before the change is delivered.
	Validation Validation `json:"validation"`
	// Publishes is what the program may put anywhere but this machine.
	Publishes Publication `json:"publishes"`
	// Lands is who may authorize the change reaching a branch others or the
	// person depend on. For gated, read Delivery.PolicyForNoCodeHost with
	// whether the repository has no code host; the table row names the
	// code-host case.
	Lands Authority `json:"lands"`
}

// Validation is what a posture requires to have run. The set is closed.
type Validation string

const (
	// ValidationFullRun is all nine stages, PRD section 5. A run, whenever
	// one starts, is all nine stages; a person may skip a stage for one run,
	// and no configuration skips one (P2).
	ValidationFullRun Validation = "full-run"
	// ValidationNone is no run at all: the task's own push starts none, so
	// nothing is validated and nothing it delivers is a pass. The phrase
	// table words such a landing as made without validation.
	ValidationNone Validation = "none"
)

// Publication is what a posture may publish. The set is closed.
type Publication string

const (
	// PublishBranchAndPullRequest is the run's anchored update of its branch
	// on the upstream, or on the fork the run names, and a pull request on
	// the code host when the upstream has one this build talks to. With no
	// code host the branch alone is published, and the pull request and
	// checks stages are skipped for that reason.
	PublishBranchAndPullRequest Publication = "branch-and-pull-request"
	// PublishNothing is nothing beyond this machine: the worker's push
	// reaches the gate's bare repository and the landing moves the person's
	// local default branch, and neither leaves the machine. It is what the
	// program does, not a fence around the worker: the copy's remote set is
	// asserted exact at dispatch and at launch, and a worker that adds a
	// remote afterwards and pushes to it is outside what this states.
	PublishNothing Publication = "nothing"
)

// Authority is who may authorize a posture's change landing. The set is
// closed.
type Authority string

const (
	// AuthorityCodeHost is the code host's merge policy on a repository this
	// build talks to through internal/forge. The program asks for a merge
	// only for a run that ended checks-passed in a repository the
	// operator's global-only autonomy.merge lists, bound to the head it
	// validated, and a refusal by the code host is reported and never
	// retried around; otherwise a person merges on the code host. Who may
	// merge there is the code host's to decide, not this program's.
	AuthorityCodeHost Authority = "code-host"
	// AuthorityRemote is the person's merge on the upstream git remote when
	// the repository's upstream is a local path or a file:// remote, so
	// there is no code host. A gated delivery task on such a repository ends
	// its run pushed after the branch update, and nothing in this program
	// merges it; the next action is the person's on the remote. An upstream
	// on a network host this build does not address is neither this nor
	// AuthorityCodeHost: no run of it starts.
	AuthorityRemote Authority = "remote"
	// AuthorityApproval is an approve answer on the approval hold the task's
	// push raised, after which the program makes the anchored fast-forward
	// of the person's local default branch. Nothing else lands it. The
	// residual gap is who gives the answer: it arrives through task.respond,
	// which a coordinator acting for the person can also call, and is
	// recorded as the machine interface's, because no surface in this build
	// witnesses a person.
	AuthorityApproval Authority = "approval-hold"
)

// policies is the one table of delivery postures, strictest first. Its
// order is what Delivery.Lighter compares, its members are what the delivery
// key decodes, and its rows are what Delivery.Policy states. The first row
// is the default, DefaultDelivery, which a test holds the two to.
var policies = []Policy{
	{DeliveryGated, ValidationFullRun, PublishBranchAndPullRequest, AuthorityCodeHost},
	{DeliveryLocal, ValidationNone, PublishNothing, AuthorityApproval},
}

// withheld is a posture PRD section 6 names and this build refuses by name,
// with the reason a refusal states. A withheld posture has no Policy: what
// it would require, publish and leave to whom is the decision that would
// promote it, and this table does not make it.
type withheld struct {
	Delivery Delivery
	Reason   string
}

// withheldDeliveries is every posture refused by name. direct is PRD section
// 12's deferred mode, the one mode that would share unvalidated work.
var withheldDeliveries = []withheld{
	{"direct", "is deferred and no configuration selects it yet"},
}

// Policy returns what d means, and false for a value outside the closed set,
// a withheld posture included.
func (d Delivery) Policy() (Policy, bool) {
	if i := d.index(); i >= 0 {
		return policies[i], true
	}
	return Policy{}, false
}

// PolicyForNoCodeHost returns what d means on a delivery task, given whether
// the repository has no code host (its upstream is a local path or a
// file:// remote), and false when d is not selectable. For gated,
// noCodeHost true states remote landing authority; false leaves the table
// row's code-host case, which holds only for a repository on a host this
// build addresses, so a caller passes false only for one of those.
func (d Delivery) PolicyForNoCodeHost(noCodeHost bool) (Policy, bool) {
	p, ok := d.Policy()
	if !ok {
		return Policy{}, false
	}
	if p.Delivery == DeliveryGated && noCodeHost {
		p.Lands = AuthorityRemote
	}
	return p, true
}

// Policies returns every posture's row, strictest first. The result is a
// copy.
func Policies() []Policy {
	out := make([]Policy, len(policies))
	copy(out, policies)
	return out
}

// errSharesUnvalidated and errLandsUnasked are the two shapes check refuses.
var (
	errSharesUnvalidated = errors.New("a posture that validates nothing publishes something")
	errLandsUnasked      = errors.New("a posture that validates nothing lands without an approval hold")
)

// check is the rule every selectable posture is held to, and the reason
// direct is withheld rather than a row: a posture that validates nothing
// publishes nothing and lands only on an approve answer. A row that broke it
// would be the direct mode or something like it, which is the owner's
// decision to promote and not a table edit. It returns nil for a row that
// keeps the rule.
func (p Policy) check() error {
	if p.Validation != ValidationNone {
		return nil
	}
	if p.Publishes != PublishNothing {
		return errSharesUnvalidated
	}
	if p.Lands != AuthorityApproval {
		return errLandsUnasked
	}
	return nil
}
